package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
)

type diagnosticExitAgent struct{ *acptest.ReplayAgent }

func (*diagnosticExitAgent) Prompt(context.Context, sdkacp.PromptRequest) (sdkacp.PromptResponse, error) {
	fmt.Fprintln(os.Stderr, "adapter diagnostic tail")
	os.Exit(9)
	return sdkacp.PromptResponse{}, nil
}

func TestACPAdapterExitRecordsCodeAndStderr(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_DIAGNOSTIC_ADAPTER") == "1" {
		agent := &diagnosticExitAgent{acptest.NewReplayAgent(nil)}
		connection := sdkacp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
		agent.SetConnection(connection)
		<-connection.Done()
		return
	}
	for _, unexpected := range []bool{true, false} {
		t.Run(fmt.Sprint(unexpected), func(t *testing.T) {
			queue := obs.NewQueue()
			ctx, cancel := context.WithTimeout(obs.WithSink(context.Background(), queue, obs.Metadata{Proc: "worker"}), 10*time.Second)
			defer cancel()
			directory := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestACPAdapterExitRecordsCodeAndStderr$")
			launch := WorkerLaunch{Session: SessionInfo{Harness: "fixture"}, Environment: append(os.Environ(), "AGENTPACK_TEST_DIAGNOSTIC_ADAPTER=1")}
			session, cleanup, err := startACPAgentProcess(ctx, launch, base.LaunchContext{WorkspaceRoot: directory}, base.Definition{}, base.ACPLaunch{Cmd: command}, directory, acp.NewEventLog(0))
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if !unexpected {
				cleanup()
				if len(queue.Events) != 0 {
					t.Fatal("requested shutdown reported an unexpected exit")
				}
				return
			}
			if err := session.Prompt(ctx, acp.PromptInput{Text: "private prompt"}); err == nil {
				t.Fatal("adapter exit did not fail the prompt")
			}
			for {
				select {
				case event := <-queue.Events:
					if event.Kind != "worker" {
						continue
					}
					encoded, _ := json.Marshal(event)
					if event.Name != "unexpected_exit" || event.Status != 9 || event.Level != "error" || !bytes.Contains(encoded, []byte("adapter diagnostic tail")) || bytes.Contains(encoded, []byte("private prompt")) {
						t.Fatalf("adapter exit event %s", encoded)
					}
					return
				case <-ctx.Done():
					t.Fatal("adapter exit event timeout")
				}
			}
		})
	}
}

func workerDiagnosticEvents(t *testing.T, home string) []obs.Event {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(home, "logs", "spool", "worker-*.ndjson"))
	if err != nil || len(files) == 0 {
		t.Fatalf("worker spools=%v err=%v", files, err)
	}
	var events []obs.Event
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var event obs.Event
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
	}
	return events
}

func TestDiagnosticStderrKeepsOnlyTheBoundedRedactedTail(t *testing.T) {
	var tail diagnosticStderr
	data := bytes.Repeat([]byte("0123456789"), 10000)
	for i := 0; i < len(data); i += 7 {
		chunk := data[i:min(len(data), i+7)]
		if n, err := tail.Write(chunk); n != len(chunk) || err != nil {
			t.Fatal(n, err)
		}
	}
	if got := tail.String(); got != string(data[len(data)-(8<<10):]) {
		t.Fatal("stderr tail lost or retained the wrong bytes")
	}
	ctx := obs.WithSink(context.Background(), obs.NewQueue(), obs.Metadata{})
	_, event := obs.Start(ctx, "worker", "exit")
	event.Set("stderr", tail.Redacted())
	clean := event.Sanitized()
	encoded, err := json.Marshal(clean.Attrs["stderr"])
	if err != nil || len(encoded) < 8<<10 {
		t.Fatalf("stderr lost its second chunk: %d bytes, %v", len(encoded), err)
	}
	tail.Write([]byte("Authorization: Bearer secret-bearer-value"))
	encoded, err = json.Marshal(tail.Redacted())
	if err != nil || strings.Contains(string(encoded), "secret-bearer-value") {
		t.Fatal("stderr leaked credentials", err)
	}
	tail.Write(bytes.Repeat([]byte("z"), 1<<20))
	if tail.String() != strings.Repeat("z", 8<<10) {
		t.Fatal("oversized write broke tail bound")
	}
}

func TestConversationTurnSummarySurvivesReplayEvictionWithoutChat(t *testing.T) {
	queue := obs.NewQueue()
	ctx := obs.WithSink(context.Background(), queue, obs.Metadata{Proc: "worker"})
	ctx = obs.WithTrace(ctx, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	log := acp.NewEventLog(512)
	var fail bool
	broker := acp.NewBroker(func(ctx context.Context, input acp.PromptInput) error {
		log.Append("prompt", 1, mustRawJSON(map[string]string{"text": input.Text}))
		for range 50 {
			log.Append("agent_message_chunk", 2, mustRawJSON(map[string]string{"text": "private reply text"}))
		}
		log.Append("tool_call", 3, json.RawMessage(`{"toolCallId":"a","title":"private tool arguments"}`))
		log.Append("tool_call_update", 4, json.RawMessage(`{"toolCallId":"a"}`))
		log.Append("permission_request", 5, json.RawMessage(`{"requestId":"a"}`))
		if fail {
			log.Append("turn_end", 6, json.RawMessage(`{"stopReason":"private response metadata"}`))
			return fmt.Errorf("adapter failed: %w", errors.New("provider unavailable"))
		}
		log.Append("turn_end", 6, json.RawMessage(`{"stopReason":"end_turn"}`))
		return nil
	}, nil, nil, nil)
	session := NewConversationSession(log, broker)
	session.sessionID = "session-test"
	for turn := range 2 {
		fail = turn == 1
		if err := session.Prompt(ctx, acp.PromptInput{Text: "private prompt text"}); (err != nil) != fail {
			t.Fatal(err)
		}
		event := <-queue.Events
		if event.Kind != "acp.turn" || event.SessionID != "session-test" || event.TraceID != obs.ParseTraceParent(obs.TraceParent(ctx)).ID || event.DurationUS < 0 {
			t.Fatalf("turn identity %+v", event)
		}
		if event.Attrs["first_seq"] != int64(1+turn*55) || event.Attrs["last_seq"] != int64((turn+1)*55) || event.Attrs["tool_calls"] != uint64(1) || event.Attrs["permission_requests"] != uint64(1) {
			t.Fatalf("turn counters %+v", event.Attrs)
		}
		if fail != (event.Level == "error") || fail != (event.Fingerprint != "") || (!fail && event.Attrs["stop_reason"] != "end_turn") {
			t.Fatalf("turn result %+v", event)
		}
		encoded, _ := json.Marshal(event)
		for _, private := range []string{"private prompt text", "private reply text", "private tool arguments", "private response metadata"} {
			if bytes.Contains(encoded, []byte(private)) {
				t.Fatal("turn summary contains chat", private)
			}
		}
	}
	if !log.Truncated() || len(queue.Events) != 0 {
		t.Fatal("fixture did not evict replay, or emitted extra turn events")
	}
}

func TestConversationTurnCancellationAndOverlap(t *testing.T) {
	queue := obs.NewQueue()
	ctx, cancel := context.WithCancel(obs.WithSink(context.Background(), queue, obs.Metadata{}))
	defer cancel()
	started := make(chan struct{})
	session := NewConversationSession(nil, acp.NewBroker(func(ctx context.Context, _ acp.PromptInput) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, nil, nil, nil))
	done := make(chan error, 1)
	go func() { done <- session.Prompt(ctx, acp.PromptInput{Text: "private"}) }()
	<-started
	if err := session.Prompt(ctx, acp.PromptInput{Text: "overlap"}); err == nil {
		t.Fatal("accepted overlapping ACP turns")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	event := <-queue.Events
	if event.Level != "error" || event.Attrs["stop_reason"] != "cancelled" || event.Attrs["first_seq"] != int64(0) || event.Attrs["last_seq"] != int64(0) {
		t.Fatalf("cancelled turn %+v", event)
	}
}

func TestWorkerTurnUsesRequestTraceAndProcessRecording(t *testing.T) {
	queue := obs.NewQueue()
	process := obs.WithSink(context.Background(), queue, obs.Metadata{Proc: "worker", Version: "test"})
	session := NewConversationSession(nil, acp.NewBroker(nil, nil, nil, nil))
	session.sessionID = "session-http"
	secret := strings.Repeat("s", 64)
	worker, err := WorkerHandler(process, nil, session, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/conversation/prompt", strings.NewReader(`{"text":"private input"}`))
	request.Header.Set("Authorization", "Bearer "+secret)
	const header = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	request.Header.Set("traceparent", header)
	response := httptest.NewRecorder()
	worker.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	event := <-queue.Events
	if event.Kind != "acp.turn" || event.SessionID != "session-http" || event.Proc != "worker" || event.TraceID != obs.ParseTraceParent(header).ID || event.Attrs["version"] != "test" {
		t.Fatalf("request turn event %+v", event)
	}
}
