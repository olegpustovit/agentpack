package sync

import (
	"net"
	"testing"
	"time"
)

// The "agentpack: add… (3m41s elapsed)" hang: a server that accepts the
// connection and never answers must fail the request, not hold the command
// forever. Every GitHub and npm request of add, lock and sync uses this client.
func TestStalledServerFailsInsteadOfHanging(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close() // hold it open, say nothing
		}
	}()
	previous := responseHeaderTimeout
	responseHeaderTimeout = 50 * time.Millisecond
	defer func() { responseHeaderTimeout = previous }()
	done := make(chan error, 1)
	go func() {
		response, err := NewService().Client.Get("http://" + listener.Addr().String() + "/repos/o/r/commits/main")
		if err == nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled server answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request is still waiting on a stalled server")
	}
}
