package network

import (
	"context"
	"encoding/json"
	"github.com/olegpustovit/agentpack/internal/session"
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
	if err := session.UpsertSession(t.Context(), auth.store.DB, session.SessionInfo{ID: id}); err != nil {
		t.Fatal(err)
	}
	session := &Session{state: session.TerminalState{Running: true, Interactive: true}, done: make(chan struct{}), attachments: make(map[uint64]*Attachment)}
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

	// Codex announces its turns in its own output (OSC 7501), even split across reads.
	session.scanAgentState([]byte("output\x1b]7501;state=wor"))
	session.scanAgentState([]byte("king:app=codex\x07more"))
	if state() != "working" {
		t.Fatalf("OSC working report: %q", state())
	}
	session.scanAgentState([]byte("\x1b]7501;state=clear:app=codex\x1b\\ \x1b]7501;state=done:app=codex\x1b\\"))
	if state() != "idle" {
		t.Fatalf("OSC idle report: %q", state())
	}
	// A repeated report never rewrites the row.
	if _, err := auth.store.DB.Exec("UPDATE sessions SET conversation_state='needs_you' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	session.scanAgentState([]byte("\x1b]7501;state=idle:app=codex\x07"))
	if state() != "needs_you" {
		t.Fatalf("a repeated report rewrote the state: %q", state())
	}
}
