//go:build linux || darwin

package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayRestartOperationIsIdempotent(t *testing.T) {
	secret := strings.Repeat("a", 64)
	worker, err := WorkerHandler(testSession(t), secret)
	if err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	worker.start = func(rows, columns uint16) (*Session, error) {
		starts.Add(1)
		command := exec.Command(executable, "-test.run=^TestTerminalChild$")
		command.Env = append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1")
		return StartSession(command, rows, columns)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = worker.Stop(ctx)
	})
	var dropResponse atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dropResponse.CompareAndSwap(true, false) {
			worker.ServeHTTP(httptest.NewRecorder(), r)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		worker.ServeHTTP(w, r)
	}))
	defer server.Close()
	home := t.TempDir()
	record := WorkerRecord{ID: "session-restart", Secret: secret, Address: strings.TrimPrefix(server.URL, "http://")}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "network", "sessions", record.ID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	gateway := (&Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: secret}, options: GatewayOptions{Home: home}}).Handler()
	request := func(operationID string) int {
		body, _ := json.Marshal(restartOperation{OperationID: operationID})
		r := httptest.NewRequest("POST", origin.String()+"/api/sessions/"+record.ID+"/restart", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		return w.Code
	}
	for _, invalid := range []string{"", "short", strings.Repeat("x", 129), "invalid/key-123456789"} {
		if status := request(invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid operation accepted: %d", status)
		}
	}
	operationID := rand.Text()
	results := make(chan int, 8)
	for range 8 {
		go func() { results <- request(operationID) }()
	}
	for range 8 {
		if status := <-results; status != http.StatusOK {
			t.Fatalf("restart returned %d", status)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("same operation started %d generations", starts.Load())
	}
	if status := request(operationID); status != http.StatusOK || starts.Load() != 1 {
		t.Fatal("retry repeated restart", status, starts.Load())
	}
	if status := request(rand.Text()); status != http.StatusOK || starts.Load() != 2 {
		t.Fatal("new operation did not restart", status, starts.Load())
	}
	operationID = rand.Text()
	dropResponse.Store(true)
	if status := request(operationID); status != http.StatusBadGateway || starts.Load() != 3 {
		t.Fatal("lost restart response was not reported as ambiguous", status, starts.Load())
	}
	if status := request(operationID); status != http.StatusOK || starts.Load() != 3 {
		t.Fatal("retry after lost response restarted twice", status, starts.Load())
	}
}

func TestWorkerRestartCacheRetainsFailuresAndIsBounded(t *testing.T) {
	worker := &WorkerHTTP{finished: true}
	operationID := "operation-00000000"
	first := worker.Restart(context.Background(), operationID)
	if first == nil || worker.Restart(context.Background(), operationID) != first {
		t.Fatal("failed operation was not cached")
	}
	for i := 1; i <= 128; i++ {
		_ = worker.Restart(context.Background(), fmt.Sprintf("operation-%08d", i))
	}
	if len(worker.restarts) != 128 || len(worker.restartIDs) != 128 {
		t.Fatal("restart history is unbounded")
	}
	if _, found := worker.restarts[operationID]; found {
		t.Fatal("oldest restart was not evicted")
	}
}
