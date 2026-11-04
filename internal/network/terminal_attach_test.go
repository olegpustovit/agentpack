package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestAttachTerminalUsesLocalViewerAndResumeOffset(t *testing.T) {
	const id = "session-attach"
	secret := strings.Repeat("s", 64)
	type requestInfo struct {
		path, authorization, local string
	}
	seen := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(TerminalState{Running: true, Interactive: true})
			return
		}
		if r.URL.Path != "/attach" {
			http.NotFound(w, r)
			return
		}
		seen <- requestInfo{r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Agentpack-Local-Terminal")}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"attached","attachment":1}`))
	}))
	defer server.Close()

	home := t.TempDir()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	record := WorkerRecord{
		ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret,
		Session: SessionInfo{ID: id, Transport: "terminal"},
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := AttachTerminal(context.Background(), home, id, 123)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	kind, frame, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !strings.Contains(string(frame), `"type":"attached"`) {
		t.Fatalf("attached frame: kind=%d data=%q err=%v", kind, frame, err)
	}
	request := <-seen
	if request.path != "/attach?since=123" || request.authorization != "Bearer "+secret || request.local != "1" {
		t.Fatalf("attach request = %+v", request)
	}
}

func TestTryReattachResumesFromLastSeenOffset(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	seenSince := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(TerminalState{Running: true, Interactive: true})
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
		_ = conn.WriteJSON(terminalNotice{Type: "attached", State: TerminalState{Running: true}, Offset: 103})
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("def"))
		_ = conn.WriteJSON(terminalNotice{Type: "replay_complete", State: TerminalState{Running: true}, Offset: 106})
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
