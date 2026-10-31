package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
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
			_, err := runner.startRemoteSession(context.Background(), home, "must-not-execute", network.SessionInfo{Harness: test.harness, Directory: test.directory})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
	data, err := json.Marshal([]network.SessionInfo{{Directory: directory, Harness: "codex", FolderID: "replaced-folder"}})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "network")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "recent.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runner.startRemoteSession(context.Background(), home, "must-not-execute", network.SessionInfo{Harness: "codex", Directory: directory})
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
	command := exec.Command(os.Args[0], "-test.run=^TestNativePresenceCLIChild$")
	command.Env = append(os.Environ(), "AGENTPACK_TEST_NATIVE_EXIT=1")
	runner := Runner{Launch: func(process *os.Process) (func(), error) {
		return network.RegisterPresence(home, network.SessionInfo{Directory: directory, Harness: "codex"}, process)
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
