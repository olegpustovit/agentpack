package network

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/tmaxmax/go-sse"
)

type conversationStreamWriter struct {
	header http.Header
	writes chan string
	status int
}

func (writer *conversationStreamWriter) Header() http.Header    { return writer.header }
func (writer *conversationStreamWriter) WriteHeader(status int) { writer.status = status }
func (writer *conversationStreamWriter) Write(data []byte) (int, error) {
	writer.writes <- string(data)
	return len(data), nil
}
func (*conversationStreamWriter) Flush() {}

func TestConversationGatewayProxyStreamsUntilCancelled(t *testing.T) {
	workerClosed := make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversation" || r.URL.Query().Get("stream") != "1" {
			http.Error(w, "wrong worker path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		message := sse.Message{}
		message.AppendData(`{"seq":8,"ts":1,"kind":"prompt","payload":{}}`)
		_, _ = message.WriteTo(w)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(workerClosed)
	}))
	defer worker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/session-peer/conversation?stream=1", nil).WithContext(ctx)
	writer := &conversationStreamWriter{header: make(http.Header), writes: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		proxyWorkerConversation(writer, request, WorkerRecord{Address: strings.TrimPrefix(worker.URL, "http://")}, "")
		close(done)
	}()
	var got string
	for !strings.Contains(got, `"seq":8`) {
		got += <-writer.writes
	}
	if writer.status != http.StatusOK {
		t.Fatalf("stream status = %d", writer.status)
	}
	cancel()
	<-workerClosed
	<-done
}

func TestConversationGatewayProxyForwardsSSEAccept(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Last-Event-ID") != "8" {
			http.Error(w, "missing SSE negotiation", http.StatusBadRequest)
			return
		}
		if r.Header.Get("traceparent") != "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01" {
			http.Error(w, "missing request trace", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
	}))
	defer worker.Close()
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/session-peer/conversation", nil)
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Last-Event-ID", "8")
	r = r.WithContext(obs.WithTrace(r.Context(), "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"))
	w := httptest.NewRecorder()
	proxyWorkerConversation(w, r, WorkerRecord{Address: strings.TrimPrefix(worker.URL, "http://")}, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" || w.Body.String() != "data: {}\n\n" {
		t.Fatalf("SSE negotiation changed: %d %s", w.Code, w.Body.String())
	}
}
func TestConversationStreamResumesAndKeepsConnectionAlive(t *testing.T) {
	old := conversationHeartbeatInterval
	conversationHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { conversationHeartbeatInterval = old })
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{}`))
	log.Append("turn_end", 2, json.RawMessage(`{}`))
	worker := &WorkerHTTP{}
	conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/conversation?stream=1&since=0", nil).WithContext(ctx)
	r.Header.Set("Last-Event-ID", "1")
	w := &conversationStreamWriter{header: make(http.Header), writes: make(chan string, 8)}
	done := make(chan struct{})
	go func() {
		worker.serveConversationSSE(w, r, conv)
		close(done)
	}()
	var output string
	for !strings.Contains(output, "ping") {
		select {
		case data := <-w.writes:
			output += data
		case <-ctx.Done():
			t.Fatal("conversation stream did not send a heartbeat")
		}
	}
	cancel()
	for {
		select {
		case <-w.writes:
		case <-done:
			if !strings.Contains(output, "id: 2") || strings.Contains(output, "id: 1") {
				t.Fatalf("replay did not resume after ID 1: %q", output)
			}
			return
		}
	}
}

func TestConversationTruncationEventIsOneShot(t *testing.T) {
	var output bytes.Buffer
	sent := false
	for range 2 {
		if err := writeTruncatedSSE(&output, true, &sent); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(output.String(), "truncated"); got != 1 {
		t.Fatalf("truncated events = %d, want 1: %q", got, output.String())
	}
}
func TestConversationSnapshotPreservesTruncationHeader(t *testing.T) {
	log := acp.NewEventLog(0)
	log.MarkTruncated()
	worker := &WorkerHTTP{conversation: NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))}
	mux := http.NewServeMux()
	worker.registerConversation(mux)
	request := httptest.NewRequest(http.MethodGet, "/conversation?since=0", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Header().Get("X-Agentpack-Truncated") != "true" {
		t.Fatalf("truncation header = %q", response.Header().Get("X-Agentpack-Truncated"))
	}
}

func TestACPAgentExitStopsStateButKeepsConversationAndRestart(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{}`))
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	worker, err := WorkerHandler(context.Background(), nil, conversation, strings.Repeat("s", 64), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restarted := false
	worker.restartACP = func(context.Context) error {
		restarted = true
		return nil
	}
	appendACPAgentExit(log)

	state := httptest.NewRecorder()
	stateRequest := httptest.NewRequest(http.MethodGet, "/state", nil)
	stateRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	worker.ServeHTTP(state, stateRequest)
	var got session.TerminalState
	if err := json.Unmarshal(state.Body.Bytes(), &got); err != nil || got.Running {
		t.Fatalf("state after agent exit = %s, err=%v", state.Body.String(), err)
	}

	transcript := httptest.NewRecorder()
	transcriptRequest := httptest.NewRequest(http.MethodGet, "/conversation", nil)
	transcriptRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	worker.ServeHTTP(transcript, transcriptRequest)
	if transcript.Code != http.StatusOK || !strings.Contains(transcript.Body.String(), `"kind":"agent_exit"`) {
		t.Fatalf("conversation after agent exit = status %d, body %s", transcript.Code, transcript.Body.String())
	}

	restart := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/restart", nil)
	request.Header.Set("Idempotency-Key", "restart-operation-0001")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	worker.ServeHTTP(restart, request)
	if restart.Code != http.StatusOK || !restarted {
		t.Fatalf("restart after agent exit = status %d, invoked=%v", restart.Code, restarted)
	}
	replayed := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	if !replayed.AgentExited() {
		t.Fatal("replayed agent_exit did not mark the adapter stopped")
	}
	if err := replayed.Prompt(context.Background(), acp.PromptInput{Text: "after exit"}); err == nil {
		t.Fatal("prompt after agent exit succeeded")
	}
}
func TestConversationSnapshotRespectsGatewayByteBoundary(t *testing.T) {
	for _, lineSize := range []int{maxConversationSnapshotBytes, maxConversationSnapshotBytes + 1} {
		name := "exact"
		if lineSize > maxConversationSnapshotBytes {
			name = "over"
		}
		t.Run(name, func(t *testing.T) {
			entry := acp.LogEntry{Seq: 1, TS: 1, Kind: "tool_call_update", Payload: json.RawMessage(`{"body":""}`)}
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			bodySize := lineSize - len(encoded) - 1
			entry.Payload = json.RawMessage(`{"body":"` + strings.Repeat("x", bodySize) + `"}`)

			log := acp.NewEventLog(maxConversationSnapshotBytes)
			log.Append(entry.Kind, entry.TS, entry.Payload)
			worker := &WorkerHTTP{conversation: NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))}
			mux := http.NewServeMux()
			worker.registerConversation(mux)
			upstream := httptest.NewServer(mux)
			defer upstream.Close()

			request := httptest.NewRequest(http.MethodGet, "/api/sessions/session/conversation", nil)
			response := httptest.NewRecorder()
			proxyWorkerConversation(response, request, WorkerRecord{Address: strings.TrimPrefix(upstream.URL, "http://")}, "")

			entries, err := readConversationNDJSON(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatalf("snapshot is not valid NDJSON: %v", err)
			}
			if lineSize == maxConversationSnapshotBytes {
				if response.Code != http.StatusOK || response.Body.Len() != maxConversationSnapshotBytes || len(entries) != 1 || entries[0].Seq != 1 {
					t.Fatalf("exact-boundary snapshot = status %d, %d bytes, %d entries", response.Code, response.Body.Len(), len(entries))
				}
				return
			}
			if response.Code != http.StatusOK || response.Body.Len() != 0 || len(entries) != 0 || response.Header().Get("X-Agentpack-Truncated") != "true" {
				t.Fatalf("over-bound snapshot = status %d, %d bytes, %d entries, truncated=%q", response.Code, response.Body.Len(), len(entries), response.Header().Get("X-Agentpack-Truncated"))
			}
		})
	}

	t.Run("gateway drops incomplete row", func(t *testing.T) {
		first, err := json.Marshal(acp.LogEntry{Seq: 1, TS: 1, Kind: "prompt", Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, '\n')
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write(first)
			_, _ = w.Write([]byte(strings.Repeat("x", maxConversationSnapshotBytes+1)))
		}))
		defer upstream.Close()

		request := httptest.NewRequest(http.MethodGet, "/api/sessions/session/conversation", nil)
		response := httptest.NewRecorder()
		proxyWorkerConversation(response, request, WorkerRecord{Address: strings.TrimPrefix(upstream.URL, "http://")}, "")
		entries, err := readConversationNDJSON(bytes.NewReader(response.Body.Bytes()))
		if err != nil || response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), first) ||
			len(entries) != 1 || entries[0].Seq != 1 || response.Header().Get("X-Agentpack-Truncated") != "true" {
			t.Fatalf("gateway snapshot = status %d, %d bytes, entries=%+v truncated=%q, err=%v", response.Code, response.Body.Len(), entries, response.Header().Get("X-Agentpack-Truncated"), err)
		}
	})
}

func TestConversationProxyPassesWorker404sThrough(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/conversation/goal" {
			http.Error(w, "no goal is set", http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/session-peer/conversation/prompt/images",
		strings.NewReader(`{"text":"look","images":[]}`),
	)
	response := httptest.NewRecorder()
	proxyWorkerConversation(response, request, WorkerRecord{Address: strings.TrimPrefix(upstream.URL, "http://")}, "/prompt/images")
	// An older worker without the route: the UI reads Go's own text as Needs update.
	if gotPath != "/conversation/prompt/images" || response.Code != http.StatusNotFound || response.Body.String() != "404 page not found\n" {
		t.Fatalf("peer path=%q response=%d %q", gotPath, response.Code, response.Body.String())
	}
	// The worker's own 404 keeps its reason, never "Needs update".
	response = httptest.NewRecorder()
	proxyWorkerConversation(response, httptest.NewRequest(http.MethodDelete, "/api/sessions/session-peer/conversation/goal", nil), WorkerRecord{Address: strings.TrimPrefix(upstream.URL, "http://")}, "/goal")
	if response.Code != http.StatusNotFound || response.Body.String() != "no goal is set\n" {
		t.Fatalf("goal response=%d %q", response.Code, response.Body.String())
	}
}

func TestConversationSnapshotIsNamedBySequence(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{"text":{"text":"hi"}}`))
	worker := &WorkerHTTP{conversation: NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))}
	mux := http.NewServeMux()
	worker.registerConversation(mux)
	get := func(match string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/conversation", nil)
		if match != "" {
			request.Header.Set("If-None-Match", match)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	first := get("")
	tag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || tag == "" {
		t.Fatalf("first read: %d etag %q", first.Code, tag)
	}
	if again := get(tag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("unchanged log: %d %q", again.Code, again.Body.String())
	}
	log.Append("agent_message_chunk", 2, json.RawMessage(`{}`))
	if changed := get(tag); changed.Code != http.StatusOK || changed.Header().Get("ETag") == tag {
		t.Fatalf("appended log answered %d with etag %q", changed.Code, changed.Header().Get("ETag"))
	}
}

func TestPackageRestartWaitsForIdleChat(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{}`))
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	worker, err := WorkerHandler(context.Background(), nil, conversation, strings.Repeat("s", 64), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restarted := make(chan struct{}, 2)
	worker.restartACP = func(context.Context) error {
		restarted <- struct{}{}
		return nil
	}
	restart := func(id string) int {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/restart?when=idle", nil)
		request.Header.Set("Idempotency-Key", id)
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
		worker.ServeHTTP(response, request)
		return response.Code
	}
	if code := restart("restart-operation-0001"); code != http.StatusOK || len(restarted) != 0 {
		t.Fatalf("busy chat: status %d, restarted %d", code, len(restarted))
	}
	log.Append("turn_end", 2, json.RawMessage(`{}`))
	<-restarted // the deferred restart runs once the turn ends
	if code := restart("restart-operation-0002"); code != http.StatusOK || len(restarted) != 1 {
		t.Fatalf("idle chat: status %d, restarted %d", code, len(restarted))
	}
}
