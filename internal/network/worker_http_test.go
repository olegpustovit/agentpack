//go:build linux || darwin

package network

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const workerTestSecret = "01234567890123456789012345678901"

func testWorkerServer(t *testing.T, session *Session) *httptest.Server {
	t.Helper()
	handler, err := WorkerHandler(session, workerTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestWorkerAuthenticationAndStateBoundary(t *testing.T) {
	session := testSession(t)
	server := testWorkerServer(t, session)
	for _, test := range []struct {
		path, token, origin string
		status              int
	}{
		{"/state", "", "", 401}, {"/attach", "", "", 401}, {"/missing", "", "", 401},
		{"/state", "wrong", "", 401}, {"/state", workerTestSecret, "https://hostile.example", 403},
		{"/attach", workerTestSecret, "http://localhost", 403}, {"/state", workerTestSecret, "", 200},
	} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+test.path, nil)
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != test.status {
			t.Fatalf("%s: %d, want %d", test.path, res.StatusCode, test.status)
		}
		if strings.Contains(string(body), workerTestSecret) {
			t.Fatal("worker token leaked")
		}
		if res.StatusCode == 200 {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			var keys []string
			for key := range fields {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			if !reflect.DeepEqual(keys, []string{"columns", "exit_code", "interactive", "restarting", "rows", "running", "shared_input"}) {
				t.Fatal(keys)
			}
		}
	}
}

func dialWorker(t *testing.T, server *httptest.Server, local bool) (*websocket.Conn, terminalNotice) {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + workerTestSecret}}
	if local {
		header.Set("X-Agentpack-Local-Terminal", "1")
	}
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/attach", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var attached terminalNotice
	if err := connection.ReadJSON(&attached); err != nil {
		t.Fatal(err)
	}
	if attached.Type != "attached" {
		t.Fatal(attached)
	}
	return connection, attached
}

func readWorker(t *testing.T, connection *websocket.Conn, matches func(int, []byte) bool) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		kind, data, err := connection.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if matches(kind, data) {
			return
		}
	}
}

func TestWorkerReportsReplayCompletionWatermark(t *testing.T) {
	session := testSession(t)
	server := testWorkerServer(t, session)
	connection, attached := dialWorker(t, server, false)
	var replayed uint64
	readWorker(t, connection, func(kind int, data []byte) bool {
		if kind == websocket.BinaryMessage {
			replayed += uint64(len(data))
			return false
		}
		var notice terminalNotice
		if err := json.Unmarshal(data, &notice); err != nil {
			t.Fatal(err)
		}
		if notice.Type != "replay_complete" {
			return false
		}
		if notice.Attachment != attached.Attachment || notice.Offset != attached.Offset+replayed {
			t.Fatalf("invalid replay watermark: %+v (%d bytes)", notice, replayed)
		}
		return true
	})
}

func TestWorkerStreamsRawPTYAndSharesInput(t *testing.T) {
	session := testSession(t)
	server := testWorkerServer(t, session)
	local, initial := dialWorker(t, server, true)
	if !initial.State.SharedInput || initial.State.Responder != initial.Attachment {
		t.Fatalf("local should be the shared-input responder: %+v", initial.State)
	}
	readWorker(t, local, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "READY")
	})
	browser, browserInitial := dialWorker(t, server, false)
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("browser-input\n")); err != nil {
		t.Fatal(err)
	}
	if err := local.WriteMessage(websocket.BinaryMessage, []byte("local-input\n")); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []*websocket.Conn{local, browser} {
		var browserOutput, localOutput string
		readWorker(t, connection, func(kind int, data []byte) bool {
			if kind == websocket.BinaryMessage {
				browserOutput += string(data)
				localOutput += string(data)
			}
			return strings.Contains(browserOutput, "ECHO browser-input") && strings.Contains(localOutput, "ECHO local-input")
		})
	}
	if browserInitial.State.Responder != initial.Attachment {
		t.Fatal("local should remain the preferred terminal-query responder")
	}
	browser.Close()
	local.Close()
	// A fresh attachment proves both the transport and the process remain usable.
	observer, attached := dialWorker(t, server, false)
	if !attached.State.Running {
		t.Fatal("disconnect stopped agent")
	}
	readWorker(t, observer, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "ECHO browser-input")
	})
}

type lockedBuffer struct {
	mu   sync.Mutex
	data strings.Builder
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.String()
}

func TestLocalTerminalReconnectsAfterDroppedConnection(t *testing.T) {
	session := testSession(t)
	secret := strings.Repeat("s", 64)
	handler, err := WorkerHandler(session, secret)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	// A proxy lets the test cut the live attachment the way a stalled socket does.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := make(chan net.Conn, 64)
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				client.Close()
				continue
			}
			accepted <- client
			go func() { io.Copy(upstream, client); upstream.Close() }()
			go func() { io.Copy(client, upstream); client.Close() }()
		}
	}()
	home := t.TempDir()
	record := WorkerRecord{ID: "session-test", Address: listener.Addr().String(), Secret: secret}
	data, _ := json.Marshal(record)
	directory := filepath.Join(home, "network", "sessions", record.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	go io.Copy(io.Discard, master)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &lockedBuffer{}
	result := make(chan error, 1)
	go func() {
		_, err := RunTerminal(ctx, home, record.ID, slave, output)
		result <- err
	}()
	waitFor := func(condition func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !condition(output.String()) {
			select {
			case err := <-result:
				t.Fatalf("terminal exited: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out; output %q", output.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(func(value string) bool { return strings.Contains(value, "READY") })
	(<-accepted).Close()
	waitFor(func(value string) bool { return strings.Count(value, "READY") >= 2 })
	// A slow attachment ends while the underlying agent remains alive.
	session.mu.Lock()
	for id, attachment := range session.attachments {
		if attachment.local {
			attachment.err = ErrSlowObserver
			close(attachment.ready)
			delete(session.attachments, id)
		}
	}
	session.updateResponderLocked()
	session.mu.Unlock()
	waitFor(func(value string) bool { return strings.Count(value, "READY") >= 3 })

	if _, err := master.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(func(value string) bool { return strings.Contains(value, "ECHO after") })
	if strings.Contains(output.String(), "agentpack: reconnecting") {
		t.Fatal("local reconnect notice was written over terminal output")
	}
}

func TestClosingLocalTerminalStopsWorker(t *testing.T) {
	for _, reconnecting := range []bool{false, true} {
		name := "connected"
		if reconnecting {
			name = "reconnecting"
		}
		t.Run(name, func(t *testing.T) {
			session := testSession(t)
			secret := strings.Repeat("s", 64)
			handler, err := WorkerHandler(session, secret)
			if err != nil {
				t.Fatal(err)
			}
			var unavailable atomic.Bool
			var attachAttempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/attach" {
					attachAttempts.Add(1)
					if unavailable.Load() {
						http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			home := t.TempDir()
			record := WorkerRecord{ID: "session-test", Address: server.Listener.Addr().String(), Secret: secret}
			data, _ := json.Marshal(record)
			directory := filepath.Join(home, "network", "sessions", record.ID)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { master.Close(); slave.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output := &lockedBuffer{}
			done := make(chan struct{})
			go func() {
				RunTerminal(ctx, home, record.ID, slave, output)
				close(done)
			}()
			waitOutput := func(want string) {
				t.Helper()
				for !strings.Contains(output.String(), want) {
					select {
					case <-ctx.Done():
						t.Fatalf("waiting for %q; got %q", want, output.String())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			waitOutput("READY")
			if reconnecting {
				unavailable.Store(true)
				handler.mu.Lock()
				for connection := range handler.connections {
					connection.Close()
				}
				handler.mu.Unlock()
				deadline := time.Now().Add(5 * time.Second)
				for {
					state := session.State()
					if state.SharedInput && state.Responder == 0 && attachAttempts.Load() >= 2 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("local client did not detach and retry: state=%+v attempts=%d", state, attachAttempts.Load())
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			master.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("closing the terminal left the local client waiting")
			}
			if session.State().Running {
				t.Fatal("closing the terminal left the agent running")
			}
		})
	}
}
