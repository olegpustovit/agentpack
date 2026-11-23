package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// Only sessions whose recorded environment moved reload; one without a record is left alone.
func TestPackageChangesRestartOnlyAffectedRunningModes(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.ReplaceModes(root, map[string]mode.Definition{"empty": {Base: mode.BaseNone}}); err != nil {
		t.Fatal(err)
	}
	worker := func(id, directory, selected string) *atomic.Int32 {
		t.Helper()
		count := new(atomic.Int32)
		var operations sync.Map
		secret := strings.Repeat("a", 64)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/state":
				_ = json.NewEncoder(w).Encode(session.TerminalState{Interactive: id != "session-headless" && id != "session-chat", Running: true})
			case "/restart":
				if r.URL.Query().Get("when") != "idle" {
					http.Error(w, "package restarts must wait for idle", 400)
					return
				}
				operationID := r.Header.Get("Idempotency-Key")
				if operationID == "" {
					http.Error(w, "missing restart operation ID", 400)
					return
				}
				if _, reused := operations.LoadOrStore(operationID, true); !reused {
					count.Add(1)
				}
				WriteJSON(w, map[string]bool{"deferred": id == "session-chat"})
			default:
				w.WriteHeader(404)
			}
		}))
		t.Cleanup(server.Close)
		record := WorkerRecord{ID: id, Secret: secret, Address: strings.TrimPrefix(server.URL, "http://"), Session: session.SessionInfo{Directory: directory, DefinitionRoot: directory, Harness: "claude", Mode: selected}}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "network", "sessions", id, "worker.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		store, err := state.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := store.DB.Exec("INSERT INTO sessions(id,node_id,directory,definition_root,harness,mode,transport) VALUES (?,"+state.LocalNodeSQL+",?,?,?,?,?)", id, directory, directory, "claude", selected, map[bool]string{true: "acp"}[id == "session-chat"])
		closeErr := store.Close()
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if selected == "" {
			selected = "default"
		}
		if id != "session-unrecorded" {
			if err := RecordLaunchEnvironment(home, id, directory, directory, selected, harness.Claude); err != nil {
				t.Fatal(err)
			}
		}
		return count
	}
	defaultRestarts := worker("session-default", root, "")
	emptyRestarts := worker("session-empty", root, "empty")
	otherRestarts := worker("session-other", t.TempDir(), "")
	headlessRestarts := worker("session-headless", root, "")
	chatRestarts := worker("session-chat", root, "")
	unrecordedRestarts := worker("session-unrecorded", root, "")
	ctx := context.Background()
	service := packSync.NewService()
	restart := func() ([]Reload, error) {
		sessions, err := Sessions(ctx, home)
		if err != nil {
			t.Fatal(err)
		}
		return RestartStaleSessions(ctx, home, service, root, sessions)
	}
	reloads, err := restart()
	if err != nil || len(reloads) != 0 || defaultRestarts.Load() != 0 {
		t.Fatalf("unchanged environment restarted: %+v %v", reloads, err)
	}
	url := "https://mcp.example/endpoint"
	if err := manifest.AddMCPServer(root, "test", mcp.Server{URL: &url}); err != nil {
		t.Fatal(err)
	}
	reloads, err = restart()
	if err != nil {
		t.Fatal(err)
	}
	if defaultRestarts.Load() != 1 || emptyRestarts.Load() != 0 || otherRestarts.Load() != 0 || headlessRestarts.Load() != 0 || chatRestarts.Load() != 1 || unrecordedRestarts.Load() != 0 {
		t.Fatalf("wrong restarts: default=%d empty=%d other=%d chat=%d unrecorded=%d", defaultRestarts.Load(), emptyRestarts.Load(), otherRestarts.Load(), chatRestarts.Load(), unrecordedRestarts.Load())
	}
	deferred := 0
	for _, reload := range reloads {
		if reload.Deferred {
			deferred++
		}
	}
	if len(reloads) != 2 || deferred != 1 {
		t.Fatalf("reloads = %+v", reloads)
	}
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restart(); err == nil {
		t.Fatal("invalid environment restarted silently")
	}
	if defaultRestarts.Load() != 1 {
		t.Fatal("invalid environment restarted")
	}
}
