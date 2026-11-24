//go:build linux || darwin

package network

import (
	"bufio"
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
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
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
	fmt.Println("STARTING CLEANUP")
	if release, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || strings.TrimSpace(release) != "finish" {
		os.Exit(8)
	}
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
		_, err := RunWorker(ctx, home, "test", WorkerLaunch{Program: executable, Directory: home, Arguments: []string{"-test.run=^TestWorkerCleanupChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_WORKER_CLEANUP=1"), Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
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
	record, err := ReadWorkerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := AttachWorker(context.Background(), record, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	readWorker(t, connection, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "READY FOR CLEANUP")
	})
	cancel()
	readWorker(t, connection, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "STARTING CLEANUP")
	})
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("finish\n")); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	readWorker(t, connection, func(kind int, data []byte) bool {
		if kind == websocket.BinaryMessage {
			output.Write(data)
			return false
		}
		var notice TerminalNotice
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
	const header = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	const claimed = "session-0123456789abcdef0123456789abcdef"
	ctx, cancel := context.WithCancel(obs.WithTrace(context.Background(), header))
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := RunWorker(ctx, home, "test", WorkerLaunch{Transport: "terminal", Session: session.SessionInfo{ID: claimed, Transport: "terminal"}, Program: executable, Directory: home, Arguments: []string{"-test.run=^TestTerminalChild$"}, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 31, Columns: 101}, func(id string) error { ready <- id; return nil })
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
	if id != claimed {
		t.Fatalf("worker used %q instead of its claimed ID %q", id, claimed)
	}
	record, err := ReadWorkerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "network", "sessions", id, "worker.json"))
	var endpoint map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &endpoint) != nil || len(endpoint) != 4 || endpoint["session"] != nil {
		t.Fatalf("worker file contains session metadata: %s %v", data, err)
	}
	stateStore := statetest.Open(t, home)
	if _, err := HideSession(ctx, stateStore, home, id); !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("archived a running session: %v", err)
	}
	renamed, err := RenameSession(ctx, stateStore, home, id, "Long-running review")
	if err != nil || renamed.ID != id || renamed.Name != "Long-running review" {
		t.Fatalf("rename running worker: %+v %v", renamed, err)
	}
	renamedRecord, err := ReadWorkerRecord(home, id)
	if err != nil || renamedRecord.ID != record.ID || renamedRecord.Address != record.Address || renamedRecord.Secret != record.Secret || renamedRecord.Session.Name != "" {
		t.Fatalf("rename changed worker identity: %+v %v", renamedRecord, err)
	}
	attach := func() *websocket.Conn {
		t.Helper()
		connection, err := AttachWorker(ctx, record, true, 0)
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
			var notice TerminalNotice
			if kind != websocket.TextMessage || json.Unmarshal(data, &notice) != nil || notice.Type != "ended" {
				return false
			}
			if !notice.State.Restarting {
				t.Fatal("restart appeared as final exit")
			}
			return true
		})
		connection.Close()
		current, err := ReadWorkerRecord(home, id)
		if err != nil || current.ID != record.ID || current.Address != record.Address || current.Secret != record.Secret {
			t.Fatalf("worker identity changed: %v", err)
		}
		connection = attach()
		sessions, err := Sessions(ctx, home)
		if err != nil || len(sessions) != 1 || sessions[0].Name != "Long-running review" {
			t.Fatalf("session name did not survive restart: %+v %v", sessions, err)
		}
		if err := connection.WriteMessage(websocket.BinaryMessage, []byte("size\n")); err != nil {
			t.Fatal(err)
		}
		readWorker(t, connection, func(kind int, data []byte) bool {
			return kind == websocket.BinaryMessage && strings.Contains(string(data), "SIZE 31 101")
		})
		if err := connection.WriteMessage(websocket.BinaryMessage, []byte("trace\n")); err != nil {
			t.Fatal(err)
		}
		readWorker(t, connection, func(kind int, data []byte) bool {
			return kind == websocket.BinaryMessage && strings.Contains(string(data), "TRACE "+obs.ParseTraceParent(header).ID)
		})
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	readWorker(t, connection, func(kind int, data []byte) bool {
		var notice TerminalNotice
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
	store := statetest.Open(t, home)
	stored, err := session.StoredSession(context.Background(), store.Reader, id)
	var ended int64
	if err != nil || stored.Name != "Long-running review" || stored.State.Running || stored.State.ExitCode != 7 || store.Reader.QueryRow("SELECT ended_at FROM sessions WHERE id=?", id).Scan(&ended) != nil || ended == 0 {
		t.Fatalf("worker exit lost durable metadata: %+v ended=%d err=%v", stored, ended, err)
	}
	if stopped, err := RenameSession(context.Background(), store, home, id, "Review complete"); err != nil || stopped.State.Running || stopped.Name != "Review complete" {
		t.Fatalf("rename stopped session: %+v %v", stopped, err)
	}
	var hidden bool
	if changed, err := HideSession(context.Background(), store, home, id); err != nil || !changed || store.Reader.QueryRow("SELECT hidden FROM sessions WHERE id=?", id).Scan(&hidden) != nil || !hidden {
		t.Fatalf("archive stopped session: hidden=%v err=%v", hidden, err)
	}
	if changed, err := HideSession(context.Background(), store, home, id); changed || err != nil {
		t.Fatalf("archive retry changed=%v err=%v", changed, err)
	}
	events := workerDiagnosticEvents(t, home)
	names := map[string]int{}
	for _, event := range events {
		names[event.Name]++
		if event.SessionID != id || event.TraceID != obs.ParseTraceParent(header).ID || event.Proc != "worker" || event.Attrs["version"] != "test" {
			t.Fatalf("worker event identity %+v", event)
		}
		if event.Name == "unexpected_exit" && (event.Status != 7 || event.Level != "error" || event.Fingerprint == "" || event.Attrs["stderr_unavailable"] == nil) {
			t.Fatalf("unexpected exit %+v", event)
		}
		if _, ok := event.Attrs["stderr"]; ok {
			t.Fatal("terminal replay copied into diagnostic stderr")
		}
	}
	if names["start"] != 1 || names["restart"] != 2 || names["unexpected_exit"] != 1 || len(events) != 4 {
		t.Fatalf("worker lifecycle events %+v", names)
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
		code, _ := RunWorker(ctx, home, "test", WorkerLaunch{Program: executable, Directory: home, Arguments: []string{"-test.run=^TestWorkerExitChild$"}, Environment: environment, Rows: 24, Columns: 80}, func(id string) error { ready <- id; return nil })
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
		record, err := ReadWorkerRecord(home, id)
		if err != nil {
			t.Fatal(err)
		}
		connection, err := AttachWorker(ctx, record, true, 0)
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
	record, err := ReadWorkerRecord(home, id)
	if err != nil {
		t.Fatal(err)
	}
	// ready confirms the endpoint, not child initialization. Allow helper startup
	// under full-suite load without shortening its final-output window.
	wait, stopWaiting := context.WithTimeout(ctx, 30*time.Second)
	defer stopWaiting()
	var state session.TerminalState
	for {
		response, err := WorkerRequest(wait, record, http.MethodGet, "/state", "")
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
	connection, err := AttachWorker(ctx, record, true, 0)
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
		var notice TerminalNotice
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
	worker, err := WorkerHandler(context.Background(), session, nil, workerTestSecret, "")
	if err != nil {
		t.Fatal(err)
	}
	worker.Start = func(rows, columns uint16) (*Session, error) {
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
		_, err := RunWorker(ctx, home, "test", WorkerLaunch{Program: executable, Arguments: []string{"-test.run=^TestTerminalChild$"}, Directory: directory, Environment: append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1"), Rows: 24, Columns: 80, Session: session.SessionInfo{Directory: directory, Harness: "codex"}}, func(id string) error { ready <- id; return nil })
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
	store := statetest.Open(t, home)
	info, err := session.StoredSession(t.Context(), store.Reader, id)
	if err != nil || info.FolderID == "" {
		t.Fatalf("missing worker identity: %v", err)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := RestartWorker(ctx, home, id, rand.Text()); !errors.Is(err, ErrWorkerControlRejected) {
		t.Fatalf("replaced folder restart: %v", err)
	}
	sessions, err := Sessions(ctx, home)
	if err != nil || len(sessions) != 1 || !sessions[0].State.Running || sessions[0].State.Restarting {
		t.Fatalf("original session interrupted: %+v %v", sessions, err)
	}
}
