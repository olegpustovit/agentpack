package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func writeReaperEndpoint(t *testing.T, home, id string, pid int, info session.SessionInfo) string {
	t.Helper()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(network.WorkerRecord{ID: id, PID: pid, Session: info, Address: "127.0.0.1:0", Secret: strings.Repeat("s", 64)})
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
	store := statetest.Open(t, home)
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

func TestReaperAuthenticatesEndpointBehindLivePID(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		healthy, fresh, replaced, ended bool
	}{
		{name: "reused PID", ended: true},
		{name: "worker answers", healthy: true},
		{name: "endpoint still starting", fresh: true},
		{name: "endpoint replaced during probe", replaced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := statetest.Open(t, home)
			const id = "session-probed"
			if err := session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id}); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, "network", "sessions", id)
			path := filepath.Join(dir, "worker.json")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			secret := strings.Repeat("s", 64)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/state" || r.Header.Get("Authorization") != "Bearer "+secret {
					t.Error("expected an authenticated state probe")
				}
				if tc.healthy {
					network.WriteJSON(w, session.TerminalState{})
					return
				}
				if tc.replaced {
					replacement := network.WorkerRecord{ID: id, PID: os.Getpid(), Address: r.Host, Secret: strings.Repeat("r", 64)}
					if err := network.PublishWorkerEndpoint(dir, replacement); err != nil {
						t.Error(err)
					}
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer worker.Close()
			record := network.WorkerRecord{ID: id, PID: os.Getpid(), Address: strings.TrimPrefix(worker.URL, "http://"), Secret: secret}
			if err := network.PublishWorkerEndpoint(dir, record); err != nil {
				t.Fatal(err)
			}
			if !tc.fresh {
				old := time.Now().Add(-2 * interruptedStartGrace)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			g := &Gateway{store: store, options: GatewayOptions{Home: home}}
			g.reapWorkerEndpoints(t.Context())
			got, err := session.StoredSession(t.Context(), store.Reader, id)
			if err != nil || !got.EndedAt.IsZero() != tc.ended {
				t.Fatalf("ended at %v, want ended=%v: %v", got.EndedAt, tc.ended, err)
			}
			if _, err := os.Stat(path); !(tc.ended && errors.Is(err, os.ErrNotExist) || !tc.ended && err == nil) {
				t.Fatalf("endpoint should be removed=%v: %v", tc.ended, err)
			}
		})
	}
}

func TestReaperEndsOnlyOldLocalTerminalsWithoutEndpoint(t *testing.T) {
	home := t.TempDir()
	store := statetest.Open(t, home)
	g := &Gateway{store: store, options: GatewayOptions{Home: home}}
	now := time.Now()
	old := now.Add(-2 * interruptedStartGrace)
	for _, info := range []session.SessionInfo{
		{ID: "session-orphan", LastUsed: old, Transport: "terminal"},
		{ID: "session-new", LastUsed: now, Transport: "shell"},
		{ID: "session-published", LastUsed: old, Transport: "terminal"},
		{ID: "session-remote", LastUsed: old, Transport: "terminal"},
		{ID: "session-chat", LastUsed: old, Transport: "acp", ConversationState: "idle"},
	} {
		if err := session.UpsertSession(t.Context(), store.DB, info); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec("UPDATE sessions SET node_id='other-node' WHERE id='session-remote'"); err != nil {
		t.Fatal(err)
	}
	writeReaperEndpoint(t, home, "session-published", os.Getpid(), session.SessionInfo{})
	g.reapStoppedChats(t.Context())
	for _, id := range []string{"session-orphan", "session-new", "session-published", "session-remote", "session-chat"} {
		info, err := session.StoredSession(t.Context(), store.Reader, id)
		if err != nil || !info.EndedAt.IsZero() != (id == "session-orphan") {
			t.Fatalf("%s ended at %v: %v", id, info.EndedAt, err)
		}
	}
}
