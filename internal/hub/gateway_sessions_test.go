package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
)

func writeHubLocalWorker(t *testing.T, home, address string) {
	t.Helper()
	const id = "session-local"
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	record := network.WorkerRecord{
		Session: network.SessionInfo{ID: id, Harness: "codex", Directory: "/local/project"},
		ID:      id, Address: address, Secret: secret,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadHubSessionsIncludesLocalAndOnlinePeer(t *testing.T) {
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/state":
			_ = json.NewEncoder(w).Encode(network.TerminalState{Running: true})
		case "/api/nodes":
			if r.URL.Query().Get("node") != "" {
				t.Errorf("node leaked outside peer session API route: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]network.TailnetNode{{ID: "peer-7", Name: "remote box", Online: true}})
		case "/api/sessions":
			if got := r.URL.Query().Get("node"); got != "peer-7" {
				t.Errorf("peer session node = %q", got)
			}
			_ = json.NewEncoder(w).Encode([]network.SessionInfo{{ID: "session-peer", Harness: "claude", Directory: "/peer/project"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	address := strings.TrimPrefix(server.URL, "http://")
	writeHubLocalWorker(t, home, address)
	nodeData, err := json.Marshal(map[string]string{"address": address, "secret": secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), nodeData, 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := loadHubSessions(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions: %+v", len(sessions), sessions)
	}
	if sessions[0].info.ID != "session-local" || sessions[0].machine != "local" {
		t.Fatalf("local session = %+v", sessions[0])
	}
	if sessions[1].info.ID != "session-peer" || sessions[1].machine != "peer-7" || sessions[1].machineName != "remote box" {
		t.Fatalf("peer session = %+v", sessions[1])
	}
}

func TestLoadHubSessionsFallsBackToLocalWithoutGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(network.TerminalState{Running: true})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubLocalWorker(t, home, strings.TrimPrefix(server.URL, "http://"))

	for _, disabled := range []bool{false, true} {
		if disabled {
			if err := network.SetGatewayEnabled(home, false); err != nil {
				t.Fatal(err)
			}
		}
		sessions, err := loadHubSessions(context.Background(), home)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || sessions[0].info.ID != "session-local" || sessions[0].machine != "local" {
			t.Fatalf("disabled=%t sessions = %+v", disabled, sessions)
		}
	}
}
