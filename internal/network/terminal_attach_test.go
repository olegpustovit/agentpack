package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/olegpustovit/agentpack/internal/session"
)

func TestTryReattachResumesFromLastSeenOffset(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	seenSince := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(session.TerminalState{Running: true, Interactive: true})
			return
		}
		if r.URL.Path != "/attach" {
			http.NotFound(w, r)
			return
		}
		seenSince <- r.URL.Query().Get("since")
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(TerminalNotice{Type: "attached", State: session.TerminalState{Running: true}, Offset: 103})
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("def"))
		_ = conn.WriteJSON(TerminalNotice{Type: "replay_complete", State: session.TerminalState{Running: true}, Offset: 106})
	}))
	defer server.Close()

	connection, err := tryReattach(context.Background(), WorkerRecord{
		Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret,
	}, 103)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if since := <-seenSince; since != "103" {
		t.Fatalf("reattach since = %q, want 103", since)
	}
	kind, data, err := connection.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !strings.Contains(string(data), `"offset":103`) {
		t.Fatalf("attached notice: kind=%d data=%q err=%v", kind, data, err)
	}
	kind, data, err = connection.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(data) != "def" {
		t.Fatalf("replay frame: kind=%d data=%q err=%v", kind, data, err)
	}
	kind, data, err = connection.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !strings.Contains(string(data), `"offset":106`) {
		t.Fatalf("replay completion: kind=%d data=%q err=%v", kind, data, err)
	}
}
