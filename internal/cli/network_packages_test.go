package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
)

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
				_ = json.NewEncoder(w).Encode(network.TerminalState{Interactive: id != "session-headless", Running: true})
			case "/restart":
				operationID := r.Header.Get("Idempotency-Key")
				if operationID == "" {
					http.Error(w, "missing restart operation ID", 400)
					return
				}
				if _, reused := operations.LoadOrStore(operationID, true); !reused {
					count.Add(1)
				}
				w.WriteHeader(200)
			default:
				w.WriteHeader(404)
			}
		}))
		t.Cleanup(server.Close)
		record := network.WorkerRecord{ID: id, Secret: secret, Address: strings.TrimPrefix(server.URL, "http://"), Session: network.SessionInfo{Directory: directory, DefinitionRoot: directory, Harness: "claude", Mode: selected}}
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
		return count
	}
	defaultRestarts := worker("session-default", root, "")
	emptyRestarts := worker("session-empty", root, "empty")
	otherRestarts := worker("session-other", t.TempDir(), "")
	headlessRestarts := worker("session-headless", root, "")
	ctx := context.Background()
	before, err := snapshotRunningEnvironments(ctx, root)
	if err != nil || len(before) != 2 {
		t.Fatalf("snapshot: %d %v", len(before), err)
	}
	runner := Runner{Stdout: io.Discard, Stderr: io.Discard}
	if err := runner.restartChangedEnvironments(ctx, before); err != nil {
		t.Fatal(err)
	}
	if defaultRestarts.Load() != 0 {
		t.Fatal("unchanged environment restarted")
	}
	url := "https://mcp.example/endpoint"
	if err := manifest.AddMCPServer(root, "test", mcp.Server{URL: &url}); err != nil {
		t.Fatal(err)
	}
	if err := runner.restartChangedEnvironments(ctx, before); err != nil {
		t.Fatal(err)
	}
	if defaultRestarts.Load() != 1 || emptyRestarts.Load() != 0 || otherRestarts.Load() != 0 || headlessRestarts.Load() != 0 {
		t.Fatalf("wrong restarts: default=%d empty=%d other=%d", defaultRestarts.Load(), emptyRestarts.Load(), otherRestarts.Load())
	}
	if err := runner.restartChangedEnvironments(ctx, before); err != nil || defaultRestarts.Load() != 1 {
		t.Fatalf("retry repeated package restart: count=%d error=%v", defaultRestarts.Load(), err)
	}
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.restartChangedEnvironments(ctx, before); err == nil {
		t.Fatal("invalid environment restarted silently")
	}
	if defaultRestarts.Load() != 1 {
		t.Fatal("invalid environment restarted")
	}
}
