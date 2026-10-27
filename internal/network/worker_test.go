//go:build linux || darwin

package network

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWorkerExitChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_WORKER_EXIT") == "1" {
		if marker := os.Getenv("AGENTPACK_TEST_WORKER_MARKER"); marker != "" {
			file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				file.Close()
				fmt.Print("INITIAL WORKER OUTPUT\n")
				_, _ = io.Copy(io.Discard, os.Stdin)
				os.Exit(0)
			}
			if !os.IsExist(err) {
				os.Exit(90)
			}
		}
		fmt.Print("FINAL WORKER OUTPUT\n")
		os.Exit(7)
	}
}

func TestWorkerCleanupChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_WORKER_CLEANUP") != "1" {
		return
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, syscall.SIGTERM)
	fmt.Println("READY FOR CLEANUP")
	<-stopped
	time.Sleep(2100 * time.Millisecond)
	fmt.Println("CREDENTIAL CLEANUP COMPLETE")
	os.Exit(0)
}

func TestRunWorkerCancellationDrainsCleanup(t *testing.T) {
	home := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, WorkerLaunch{Program: executable, Directory: home, Arguments: []string{"-test.run=^TestWorkerCleanupChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_WORKER_CLEANUP=1"), Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
		done <- err
	}()
	var id string
	select {
	case id = <-ready:
	case err := <-done:
		t.Fatalf("worker startup failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker startup timeout")
	}
	record, err := workerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := attachWorker(context.Background(), record, true)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	readWorker(t, connection, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "READY FOR CLEANUP")
	})
	cancel()
	var output strings.Builder
	readWorker(t, connection, func(kind int, data []byte) bool {
		if kind == websocket.BinaryMessage {
			output.Write(data)
			return false
		}
		var notice terminalNotice
		return json.Unmarshal(data, &notice) == nil && notice.Type == "ended"
	})
	if !strings.Contains(output.String(), "CREDENTIAL CLEANUP COMPLETE") {
		t.Fatalf("worker shutdown interrupted cleanup: %q", output.String())
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish shutdown")
	}
}

func TestRunWorkerRestartKeepsIdentity(t *testing.T) {
	home := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, WorkerLaunch{Program: executable, Directory: home, Arguments: []string{"-test.run=^TestTerminalChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 31, Columns: 101}, func(id string) error { ready <- id; return nil })
		done <- err
	}()
	var id string
	select {
	case id = <-ready:
	case err := <-done:
		t.Fatalf("worker startup failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker startup timeout")
	}
	record, err := workerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	attach := func() *websocket.Conn {
		t.Helper()
		connection, err := attachWorker(ctx, record, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.Close() })
		readWorker(t, connection, func(kind int, data []byte) bool {
			return kind == websocket.BinaryMessage && strings.Contains(string(data), "READY")
		})
		return connection
	}
	connection := attach()
	for attempt := 0; attempt < 2; attempt++ {
		if err := RestartWorker(ctx, home, id, rand.Text()); err != nil {
			t.Fatal(err)
		}
		readWorker(t, connection, func(kind int, data []byte) bool {
			var notice terminalNotice
			if kind != websocket.TextMessage || json.Unmarshal(data, &notice) != nil || notice.Type != "ended" {
				return false
			}
			if !notice.State.Restarting {
				t.Fatal("restart appeared as final exit")
			}
			return true
		})
		connection.Close()
		current, err := workerRecord(home, id)
		if err != nil || current.ID != record.ID || current.Address != record.Address || current.Secret != record.Secret {
			t.Fatalf("worker identity changed: %v", err)
		}
		connection = attach()
		if err := connection.WriteMessage(websocket.BinaryMessage, []byte("size\n")); err != nil {
			t.Fatal(err)
		}
		readWorker(t, connection, func(kind int, data []byte) bool {
			return kind == websocket.BinaryMessage && strings.Contains(string(data), "SIZE 31 101")
		})
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	readWorker(t, connection, func(kind int, data []byte) bool {
		var notice terminalNotice
		if kind != websocket.TextMessage || json.Unmarshal(data, &notice) != nil || notice.Type != "ended" {
			return false
		}
		if notice.State.Restarting || notice.State.ExitCode != 7 {
			t.Fatalf("final state: %+v", notice.State)
		}
		return true
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit")
	}
	if _, err := os.Stat(filepath.Join(home, "network", "sessions", id)); !os.IsNotExist(err) {
		t.Fatal("worker record survived exit")
	}
}

func TestRunWorkerFastExitPreservesFinalOutput(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) { runFastExitWorker(t, restart) })
	}
}

func runFastExitWorker(t *testing.T, restart bool) {
	home := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan int, 1)
	environment := append(os.Environ(), "AGENTPACK_TEST_WORKER_EXIT=1")
	if restart {
		environment = append(environment, "AGENTPACK_TEST_WORKER_MARKER="+filepath.Join(home, "started"))
	}
	go func() {
		code, _ := RunWorker(ctx, home, WorkerLaunch{Program: executable, Directory: home, Arguments: []string{"-test.run=^TestWorkerExitChild$"}, Environment: environment, Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
		done <- code
	}()
	var id string
	select {
	case id = <-ready:
	case <-done:
		t.Fatal("worker exited before ready")
	case <-time.After(10 * time.Second):
		t.Fatal("worker startup timeout")
	}
	if restart {
		record, err := workerRecord(home, id)
		if err != nil {
			t.Fatal(err)
		}
		connection, err := attachWorker(ctx, record, true)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		readWorker(t, connection, func(kind int, data []byte) bool {
			return kind == websocket.BinaryMessage && strings.Contains(string(data), "INITIAL WORKER OUTPUT")
		})
		if err := RestartWorker(ctx, home, id, rand.Text()); err != nil {
			t.Fatal(err)
		}
		connection.Close()
	}
	record, err := workerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	// ready confirms the endpoint, not child initialization. Allow helper startup
	// under full-suite load without shortening its final-output window.
	wait, stopWaiting := context.WithTimeout(ctx, 30*time.Second)
	defer stopWaiting()
	var state TerminalState
	for {
		response, err := workerRequest(wait, record, http.MethodGet, "/state", "")
		if err == nil {
			err = json.NewDecoder(response.Body).Decode(&state)
			response.Body.Close()
			if err == nil && response.StatusCode != http.StatusOK {
				err = fmt.Errorf("worker state returned HTTP %d", response.StatusCode)
			}
		}
		if err == nil && !state.Running {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("worker exited before final attachment: code=%d state=%+v error=%v", code, state, err)
		case <-wait.Done():
			t.Fatalf("short child did not exit: state=%+v error=%v", state, err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	connection, err := attachWorker(ctx, record, true)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var output strings.Builder
	readWorker(t, connection, func(kind int, data []byte) bool {
		if kind == websocket.BinaryMessage {
			output.Write(data)
			return false
		}
		var notice terminalNotice
		if json.Unmarshal(data, &notice) != nil || notice.Type != "ended" {
			return false
		}
		if notice.State.ExitCode != 7 {
			t.Fatalf("final state: %+v", notice.State)
		}
		return true
	})
	if !strings.Contains(output.String(), "FINAL WORKER OUTPUT") {
		t.Fatalf("lost final output: %q", output.String())
	}
	select {
	case code := <-done:
		if code != 7 {
			t.Fatalf("worker returned %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit")
	}
}

func TestWorkerRestartFailureEndsWithoutOverlap(t *testing.T) {
	session := testSession(t)
	worker, err := WorkerHandler(session, workerTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	worker.start = func(rows, columns uint16) (*Session, error) {
		if session.State().Running {
			t.Error("starting replacement before old agent exited")
		}
		command := exec.Command(filepath.Join(t.TempDir(), "missing"))
		return StartSession(command, rows, columns)
	}
	server := httptest.NewServer(worker)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := worker.Restart(ctx, rand.Text()); err == nil {
		t.Fatal("missing executable restarted")
	}
	select {
	case <-worker.done:
	case <-ctx.Done():
		t.Fatal("failed restart kept worker alive")
	}
	if session.State().Restarting || session.State().Running {
		t.Fatal("failed restart retained active state")
	}
	if err := worker.Restart(ctx, rand.Text()); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restart of ended worker: %v", err)
	}
}

func TestWorkerRejectsReplacedWorkspaceWithoutStoppingAgent(t *testing.T) {
	home, parent := t.TempDir(), t.TempDir()
	directory := filepath.Join(parent, "workspace")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan string, 1), make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, WorkerLaunch{Program: executable, Arguments: []string{"-test.run=^TestTerminalChild$"}, Directory: directory, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 24, Columns: 80, Session: SessionInfo{Directory: directory, Harness: "codex"}}, func(id string) error { ready <- id; return nil })
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("worker did not stop")
		}
	})
	var id string
	select {
	case id = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not start")
	}
	record, err := workerRecord(home, id)
	if err != nil || record.Session.FolderID == "" {
		t.Fatalf("missing worker identity: %v", err)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := RestartWorker(ctx, home, id, rand.Text()); !errors.Is(err, errWorkerControlRejected) {
		t.Fatalf("replaced folder restart: %v", err)
	}
	sessions, err := Sessions(ctx, home)
	if err != nil || len(sessions) != 1 || !sessions[0].State.Running || sessions[0].State.Restarting {
		t.Fatalf("original session interrupted: %+v %v", sessions, err)
	}
}
