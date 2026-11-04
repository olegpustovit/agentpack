package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchWorkerConversationState(t *testing.T) {
	home := t.TempDir()
	sessionDir := filepath.Join(home, "network", "sessions", "session-test")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := WorkerRecord{ID: "session-test", Session: SessionInfo{ConversationState: "idle"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := patchWorkerConversationState(sessionDir, "needs_you"); err != nil {
		t.Fatal(err)
	}
	var got WorkerRecord
	data, err = os.ReadFile(filepath.Join(sessionDir, "worker.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.ConversationState != "needs_you" {
		t.Fatalf("state %q", got.Session.ConversationState)
	}
	if err := patchWorkerConversationState(sessionDir, "needs_you"); err != nil {
		t.Fatal(err)
	}
}
