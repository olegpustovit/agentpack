package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type countedConn struct {
	net.Conn
	read atomic.Int64
}

func (c *countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func TestGatewayCompressesTerminalReplayWithoutChangingBytes(t *testing.T) {
	const id = "session-compression"
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", id), 0o700); err != nil {
		t.Fatal(err)
	}
	session := &Session{state: TerminalState{Running: true, Interactive: true}, replay: bytes.Repeat([]byte("terminal replay line\r\n\x1b[32mready\x1b[0m\r\n"), 1<<15), done: make(chan struct{}), attachments: make(map[uint64]*Attachment)}
	t.Cleanup(func() { close(session.done) })
	secret := strings.Repeat("s", 64)
	worker, err := WorkerHandler(session, secret)
	if err != nil {
		t.Fatal(err)
	}
	workerServer := httptest.NewServer(worker)
	t.Cleanup(workerServer.Close)
	recordData, err := json.Marshal(WorkerRecord{ID: id, Address: workerServer.Listener.Addr().String(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "sessions", id, "worker.json"), recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{auth: auth, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	server := httptest.NewServer(gateway.Handler())
	t.Cleanup(server.Close)
	gateway.origin, _ = url.Parse(server.URL)
	var counted *countedConn
	dialer := websocket.Dialer{EnableCompression: true, NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		counted = &countedConn{Conn: connection}
		return counted, nil
	}}
	requestHeader := map[string][]string{"Authorization": {"Bearer " + gateway.local.Secret}}
	connection, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/sessions/"+id+"/terminal", requestHeader)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if !strings.Contains(response.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Fatalf("compression not negotiated: %v", response.Header)
	}
	var attached terminalNotice
	if err := connection.ReadJSON(&attached); err != nil || attached.Type != "attached" {
		t.Fatalf("attached notice: %+v, %v", attached, err)
	}
	kind, replay, err := connection.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(replay, session.replay) {
		t.Fatalf("replay changed: kind=%d bytes=%d expected=%d err=%v", kind, len(replay), len(session.replay), err)
	}
	var complete terminalNotice
	if err := connection.ReadJSON(&complete); err != nil || complete.Type != "replay_complete" {
		t.Fatalf("replay completion: %+v, %v", complete, err)
	}
	if got := counted.read.Load(); got >= int64(len(session.replay))/10 {
		t.Fatalf("replay was not compressed: wire bytes=%d replay bytes=%d", got, len(session.replay))
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

var _ net.Conn = (*countedConn)(nil)
