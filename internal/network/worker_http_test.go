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
			if !reflect.DeepEqual(keys, []string{"columns", "epoch", "exit_code", "interactive", "owner", "restarting", "rows", "running"}) {
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

func TestWorkerStreamsRawPTYAndDisconnectLeavesAgent(t *testing.T) {
	session := testSession(t)
	server := testWorkerServer(t, session)
	local, initial := dialWorker(t, server, true)
	if initial.State.Owner != initial.Attachment {
		t.Fatal("local did not own input")
	}
	readWorker(t, local, func(kind int, data []byte) bool {
		return kind == websocket.BinaryMessage && strings.Contains(string(data), "READY")
	})
	browser, browserInitial := dialWorker(t, server, false)
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("unauthorized\n")); err != nil {
		t.Fatal(err)
	}
	readWorker(t, browser, func(kind int, data []byte) bool {
		if kind != websocket.TextMessage {
			return false
		}
		var notice terminalNotice
		if err := json.Unmarshal(data, &notice); err != nil {
			t.Fatal(err)
		}
		return notice.Type == "error" && notice.Error == ErrNotOwner.Error()
	})
	if err := local.WriteJSON(terminalControl{Action: "release", Epoch: initial.State.Epoch}); err != nil {
		t.Fatal(err)
	}
	readWorker(t, local, func(kind int, data []byte) bool {
		if kind != websocket.TextMessage {
			return false
		}
		var notice terminalNotice
		if err := json.Unmarshal(data, &notice); err != nil {
			t.Fatal(err)
		}
		return notice.Type == "state" && notice.State.Owner == 0
	})
	if err := browser.WriteJSON(terminalControl{Action: "take"}); err != nil {
		t.Fatal(err)
	}
	readWorker(t, browser, func(kind int, data []byte) bool {
		if kind != websocket.TextMessage {
			return false
		}
		var notice terminalNotice
		if err := json.Unmarshal(data, &notice); err != nil {
			t.Fatal(err)
		}
		return notice.Type == "state" && notice.State.Owner == browserInitial.Attachment
	})
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("browser-input\n")); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []*websocket.Conn{local, browser} {
		var output string
		readWorker(t, connection, func(kind int, data []byte) bool {
			if kind == websocket.BinaryMessage {
				output += string(data)
			}
			return strings.Contains(output, "ECHO browser-input")
		})
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
	if _, err := master.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(func(value string) bool { return strings.Contains(value, "ECHO after") })
}
