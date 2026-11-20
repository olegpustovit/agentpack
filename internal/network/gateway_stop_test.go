package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestStopGatewayWaitsForTheProcessToExit(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"stopping":true}`)) }))
	defer server.Close()
	data, _ := json.Marshal(nodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The running gateway holds its lock until it exits.
	lock := flock.New(filepath.Join(home, "network", "gateway.lock"))
	if ok, err := lock.TryLock(); !ok || err != nil {
		t.Fatal(ok, err)
	}
	released := make(chan time.Time, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		released <- time.Now()
		lock.Unlock()
	}()
	if err := StopGateway(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if exited := <-released; time.Now().Before(exited) {
		t.Fatal("stop returned before the gateway exited")
	}
}
