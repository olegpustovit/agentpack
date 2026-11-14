package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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
	if _, err := store.DB.Exec("INSERT INTO sessions(id,node_id,directory,harness,folder_id) VALUES ('session-replaced','local',?,'codex','replaced-folder')", directory); err != nil {
		t.Fatal(err)
	}
	_, err = runner.startRemoteSession(context.Background(), home, "must-not-execute", network.SessionInfo{Harness: "codex", Directory: directory}, "")
	if err == nil || !strings.Contains(err.Error(), "folder has changed") {
		t.Fatalf("replaced folder accepted: %v", err)
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
