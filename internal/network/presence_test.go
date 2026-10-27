package network

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPresenceChild(t *testing.T) {
	if os.Getenv("AGENTPACK_PRESENCE_CHILD") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func TestNativePresencePreservesPipesAndOwnsStop(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestPresenceChild$")
	command.Env = append(os.Environ(), "AGENTPACK_PRESENCE_CHILD=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	home := t.TempDir()
	cleanup, err := RegisterPresence(home, SessionInfo{Directory: t.TempDir(), Harness: "codex"}, command.Process)
	if err != nil {
		command.Process.Kill()
		command.Wait()
		t.Fatal(err)
	}
	defer cleanup()
	sessions, err := Sessions(context.Background(), home)
	if err != nil || len(sessions) != 1 || sessions[0].State.Interactive || !sessions[0].State.Running {
		t.Fatalf("presence: %+v %v", sessions, err)
	}
	if err = StopWorker(context.Background(), home, sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("native child did not stop")
	}
	cleanup()
	if sessions, err = Sessions(context.Background(), home); err != nil || len(sessions) != 0 {
		t.Fatal("presence survived cleanup", sessions, err)
	}
}
