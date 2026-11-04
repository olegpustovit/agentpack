package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
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
	worker, err := WorkerHandler(nil, conversation, strings.Repeat("s", 64), t.TempDir())
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
	var got TerminalState
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

func TestConversationPromptImagesProxyMapsOldPeer404ToNeedsUpdate(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
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
	if gotPath != "/conversation/prompt/images" || response.Code != http.StatusNotFound || response.Body.String() != "Needs update\n" {
		t.Fatalf("peer path=%q response=%d %q", gotPath, response.Code, response.Body.String())
	}
}
