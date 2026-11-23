package network

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gateway relays the worker's derived frame to the browser beside its entries.
func TestConversationRelayForwardsDerivedFrames(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	id := "session-relay"
	if err := upsertSession(t.Context(), g.store.DB, SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\ndata: {\"seq\":1,\"kind\":\"prompt\",\"payload\":{}}\n\n" +
			"event: derived\ndata: {\"conversation_state\":\"working\",\"working\":true,\"needs_you\":false,\"through\":1}\n\n"))
	}))
	defer worker.Close()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishWorkerEndpoint(directory, WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64)}); err != nil {
		t.Fatal(err)
	}
	var frames []conversationStreamFrame
	since, err := g.readConversationStream(t.Context(), id, "local", 0, func(frame conversationStreamFrame) bool {
		frames = append(frames, frame)
		return true
	})
	if err != nil || since != 1 || len(frames) != 2 || frames[0].event != "conversation" ||
		frames[1].event != "conversation_derived" || !strings.Contains(string(frames[1].body), `"through":1`) {
		t.Fatalf("since=%d err=%v frames=%+v", since, err, frames)
	}
}
