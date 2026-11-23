package network

import (
	"encoding/json"
	"errors"
	"github.com/olegpustovit/agentpack/internal/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReaperEndpoint(t *testing.T, home, id string, pid int, info session.SessionInfo) string {
	t.Helper()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(WorkerRecord{ID: id, PID: pid, Session: info, Address: "127.0.0.1:0", Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestDeadCurrentEndpointPreservesSessionHistory(t *testing.T) {
	home := t.TempDir()
	store := testSessionStore(t, home)
	info := session.SessionInfo{ID: "session-current", Name: "Current chat", Transport: "acp", ConversationState: "working"}
	if err := session.UpsertSession(t.Context(), store.DB, info); err != nil {
		t.Fatal(err)
	}
	dir := writeReaperEndpoint(t, home, info.ID, 0, session.SessionInfo{})
	transcript := filepath.Join(dir, "conversation.ndjson")
	if err := os.WriteFile(transcript, []byte("unrelated support file"), 0600); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	g.reapWorkerEndpoints(t.Context())
	got, err := session.StoredSession(t.Context(), store.Reader, info.ID)
	if err != nil || got.EndedAt.IsZero() || got.Name != info.Name || got.ConversationState != "working" {
		t.Fatalf("reaped current session: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "worker.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead endpoint remains: %v", err)
	}
	if _, err := os.Stat(transcript); err != nil {
		t.Fatal(err)
	}
}
