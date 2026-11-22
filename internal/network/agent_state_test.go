package network

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A terminal agent's hooks (ap hook-exec session-state) report turns through its worker to the session row.
func TestTerminalAgentStateReachesSessionRow(t *testing.T) {
	const id = "session-terminal-state"
	home := t.TempDir()
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSession(t.Context(), auth.store.DB, SessionInfo{ID: id}); err != nil {
		t.Fatal(err)
	}
	session := &Session{state: TerminalState{Running: true, Interactive: true}, done: make(chan struct{}), attachments: make(map[uint64]*Attachment)}
	t.Cleanup(func() { close(session.done) })
	secret := strings.Repeat("s", 64)
	worker, err := WorkerHandler(context.Background(), session, nil, secret, dir)
	if err != nil {
		t.Fatal(err)
	}
	worker.stateStore = auth.store
	server := httptest.NewServer(worker)
	t.Cleanup(server.Close)
	record, _ := json.Marshal(WorkerRecord{ID: id, Address: server.Listener.Addr().String(), Secret: secret})
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		var value string
		if err := auth.store.DB.QueryRow("SELECT conversation_state FROM sessions WHERE id=?", id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, want := range []string{"working", "needs_you", "idle"} {
		if err := ReportAgentState(t.Context(), home, id, want); err != nil || state() != want {
			t.Fatalf("report %s: err=%v state=%q", want, err, state())
		}
	}
	if err := ReportAgentState(t.Context(), home, id, "starting"); err == nil || state() != "idle" {
		t.Fatalf("an unknown state was accepted: err=%v state=%q", err, state())
	}
}
