//go:build linux || darwin

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

func TestTerminalReplayTail(t *testing.T) {
	session := testSession(t)
	session.replay = []byte("0123456789abcdef")
	session.offset = 0
	secret := strings.Repeat("s", 64)
	handler, err := WorkerHandler(session, nil, secret, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	home := t.TempDir()
	id := "session-tail"
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := WorkerRecord{ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: secret}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	tail, err := TerminalReplayTail(context.Background(), home, id, 8)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "89abcdef" {
		t.Fatalf("tail = %q", tail)
	}
}
