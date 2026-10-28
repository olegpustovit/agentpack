//go:build linux || darwin

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
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
	workspace := filepath.Join(root, "project1")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(root, "fake-codex")
	writeFile(t, agent, "#!/bin/sh\n[ \"$(pwd -P)\" = \"$TEST_WORKSPACE\" ] && [ \"$PWD\" = \"$TEST_WORKSPACE\" ] || exit 19\nprintf 'READY SHARED AGENT\\n'\nwhile IFS= read -r line; do\n case \"$line\" in exit) exit 7;; *) printf 'ECHO %s\\n' \"$line\";; esac\ndone\n")
	if err := os.Chmod(agent, 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(agentpackBinary, "codex")
	command.Dir = workspace
	command.Env = installerEnvironment("HOME="+filepath.Join(root, "user"), "USERPROFILE="+filepath.Join(root, "user"), "AGENTPACK_HOME="+home, "AGENTPACK_STAGING_ROOT="+filepath.Join(root, "staging"), "CODEX_PATH="+agent, "TEST_WORKSPACE="+workspace, "AGENTPACK_PUBLIC_URL=", "AGENTPACK_SESSION_WORKER=")
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
	rendered := false
	readUntil := func(want string) {
		t.Helper()
		var output strings.Builder
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for !strings.Contains(output.String(), want) {
			select {
			case chunk := <-chunks:
				if rendered && strings.Contains(chunk, "agentpack:") {
					t.Fatalf("transport message overwrote agent screen: %q", chunk)
				}
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
	rendered = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessions, err := network.Sessions(ctx, home)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %+v %v", sessions, err)
	}
	id := sessions[0].ID
	if !sessions[0].State.SharedInput {
		t.Fatal("session does not support shared input")
	}
	// Use the authenticated gateway websocket while the real CLI remains attached.
	data, err := os.ReadFile(filepath.Join(home, "network", "node.json"))
	if err != nil {
		t.Fatal(err)
	}
	var node struct{ Address, Secret string }
	if err := json.Unmarshal(data, &node); err != nil {
		t.Fatal(err)
	}
	browser, _, err := websocket.DefaultDialer.Dial("ws://"+node.Address+"/api/sessions/"+id+"/terminal", http.Header{"Authorization": {"Bearer " + node.Secret}})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if err := browser.ReadJSON(new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("browser input\n")); err != nil {
		t.Fatal(err)
	}
	readUntil("ECHO browser input")
	if _, err := terminal.Write([]byte("local input\n")); err != nil {
		t.Fatal(err)
	}
	readUntil("ECHO local input")
	if !sessions[0].State.Interactive || sessions[0].Directory != workspace || sessions[0].DefinitionRoot != root {
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
