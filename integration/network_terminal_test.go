//go:build linux || darwin

package integration_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/olegpustovit/agentpack/internal/network"
)

func TestCompiledCLISharedTerminalSurvivesGatewayAndRestart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "agentpack-home")
	agent := filepath.Join(root, "fake-codex")
	writeFile(t, agent, "#!/bin/sh\nprintf 'READY SHARED AGENT\\n'\nwhile IFS= read -r line; do\n case \"$line\" in exit) exit 7;; *) printf 'ECHO %s\\n' \"$line\";; esac\ndone\n")
	if err := os.Chmod(agent, 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(agentpackBinary, "codex")
	command.Dir = root
	command.Env = installerEnvironment("HOME="+filepath.Join(root, "user"), "USERPROFILE="+filepath.Join(root, "user"), "AGENTPACK_HOME="+home, "AGENTPACK_STAGING_ROOT="+filepath.Join(root, "staging"), "CODEX_PATH="+agent, "AGENTPACK_PUBLIC_URL=", "AGENTPACK_SESSION_WORKER=")
	init := exec.Command(agentpackBinary, "init")
	init.Dir = root
	init.Env = command.Env
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, output)
	}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 31, Cols: 101})
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(chan string, 128)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				chunks <- string(buffer[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, _ := network.Sessions(ctx, home)
		for _, session := range sessions {
			_ = network.StopWorker(ctx, home, session.ID)
		}
		_, _ = network.NodeRequest(ctx, home, "POST", "/api/node/stop")
		_ = command.Process.Kill()
		terminal.Close()
	})
	readUntil := func(want string) {
		t.Helper()
		var output strings.Builder
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for !strings.Contains(output.String(), want) {
			select {
			case chunk := <-chunks:
				output.WriteString(chunk)
			case <-done:
				for len(chunks) > 0 {
					output.WriteString(<-chunks)
				}
				if !strings.Contains(output.String(), want) {
					t.Fatalf("terminal ended before %q: %s", want, output.String())
				}
				return
			case <-timer.C:
				t.Fatalf("waiting for %q: %s", want, output.String())
			}
		}
	}
	readUntil("READY SHARED AGENT")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions, err := network.Sessions(ctx, home)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %+v %v", sessions, err)
	}
	id := sessions[0].ID
	if _, err := terminal.Write([]byte{29}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		sessions, err = network.Sessions(ctx, home)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) == 1 && sessions[0].State.Owner == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local input did not release")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := terminal.Write(append([]byte{29}, []byte("handoff input\n")...)); err != nil {
		t.Fatal(err)
	}
	readUntil("ECHO handoff input")
	if !sessions[0].State.Interactive || sessions[0].Directory != root {
		t.Fatalf("session metadata: %+v", sessions[0])
	}
	if _, err := network.NodeRequest(ctx, home, "POST", "/api/node/stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("after gateway stop\n")); err != nil {
		t.Fatal(err)
	}
	readUntil("ECHO after gateway stop")
	if err := network.RestartWorker(ctx, home, id, "integration-restart-1"); err != nil {
		t.Fatal(err)
	}
	readUntil("READY SHARED AGENT")
	if _, err := terminal.Write([]byte("after restart\n")); err != nil {
		t.Fatal(err)
	}
	readUntil("ECHO after restart")
	if _, err := terminal.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("exit code: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("local launcher did not preserve final exit")
	}
}
