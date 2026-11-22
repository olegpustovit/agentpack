package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestRemoteSessionRequiresRememberedFolderAndTerminalHarness(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	runner := Runner{}
	for _, test := range []struct {
		name, harness, directory, want string
	}{
		{"unknown harness", "cursor", directory, "unknown harness"},
		{"relative folder", "codex", "project", "absolute"},
		{"unknown folder", "codex", directory, "not been used before"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runner.startRemoteSession(context.Background(), home, "must-not-execute", network.SessionInfo{Harness: test.harness, Directory: test.directory}, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("INSERT INTO sessions(id,node_id,directory,harness,folder_id) VALUES ('session-replaced',(SELECT node_id FROM local_node WHERE id=1),?,'codex','replaced-folder')", directory); err != nil {
		t.Fatal(err)
	}
	_, err = runner.startRemoteSession(context.Background(), home, "must-not-execute", network.SessionInfo{Harness: "codex", Directory: directory}, "")
	if err == nil || !strings.Contains(err.Error(), "folder has changed") {
		t.Fatalf("replaced folder accepted: %v", err)
	}
}

func TestRemoteTerminalLaunchAcceptsGatewayVerifiedFolder(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	identity, err := network.FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	info := network.SessionInfo{Harness: "codex", Directory: directory, Transport: "terminal", FolderID: identity, StartedBy: &network.Actor{Kind: "browser"}}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Runner{}.startRemoteSession(t.Context(), home, filepath.Join(home, "missing-worker"), info, "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected to reach the worker launch, got %v", err)
	}
}

func TestNativePresenceCLIChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_NATIVE_EXIT") == "1" {
		os.Exit(23)
	}
}

func TestNativePresenceEndsWithLaunchProcess(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	startNativePresenceGateway(t, home)
	command := exec.Command(os.Args[0], "-test.run=^TestNativePresenceCLIChild$")
	command.Env = append(os.Environ(), "AGENTPACK_TEST_NATIVE_EXIT=1")
	runner := Runner{Launch: func(process *os.Process) (func(), error) {
		return network.RegisterPresence(home, "test", network.SessionInfo{Directory: directory, Harness: "codex"}, process)
	}}
	code, err := runner.runProcess(command)
	if code != 23 || err != nil {
		t.Fatalf("native exit: %d %v", code, err)
	}
	sessions, err := network.Sessions(context.Background(), home)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("launcher presence survived exit: %+v %v", sessions, err)
	}
	recent, err := network.Recent(home)
	if err != nil || len(recent) != 1 || recent[0].Harness != "codex" {
		t.Fatalf("native folder history missing: %+v %v", recent, err)
	}
}

func startNativePresenceGateway(t *testing.T, home string) {
	t.Helper()
	// Keep schema creation outside the launch-presence readiness deadline.
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- network.ServeGateway(ctx, network.GatewayOptions{Home: home, Address: "127.0.0.1:0"}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("gateway did not stop")
		}
	})
	ready, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := network.NodeRequest(ready, home, "GET", "/healthz"); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-ready.Done():
			t.Fatal("gateway did not start")
		}
	}
}

// A folder that holds its own agentpack.toml records no definition root. A chat
// started there must stage that folder before the worker launches the agent;
// otherwise Codex gets a CODEX_HOME that was never built.
func TestRemoteChatStagesFolderWithoutDefinitionRoot(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(directory, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(directory, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	folder, err := network.FolderIdentity(directory)
	if err != nil {
		t.Skip(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO sessions(id,node_id,directory,harness,folder_id) VALUES ('session-known',(SELECT node_id FROM local_node WHERE id=1),?,'codex',?)", directory, folder); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// The worker cannot start from a missing executable; the sync before it is what is tested.
	_, _ = Runner{}.startRemoteSession(context.Background(), home, filepath.Join(t.TempDir(), "missing"), network.SessionInfo{Harness: "codex", Directory: directory, Transport: "acp"}, "")
	codexHome, err := codex.CurrentHome(directory, "default")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(codexHome, "config.toml")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Codex home %s was not staged: %v", codexHome, err)
	}
}

func TestRemoteACPPreparationOverlapsAndJoinsBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	task := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			started <- name
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	done := make(chan error, 1)
	go func() { done <- prepareRemoteACP(ctx, task("stage"), task("adapter")) }()
	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-ctx.Done():
			t.Fatalf("both preparations did not run together: %v", seen)
		}
	}
	if !seen["stage"] || !seen["adapter"] {
		t.Fatalf("preparations started: %v", seen)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
