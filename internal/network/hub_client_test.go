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

	"github.com/tmaxmax/go-sse"
)

func writeHubNode(t *testing.T, home, address string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(nodeRecord{Address: address, Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeConversationUsesNodeQueryAndCancels(t *testing.T) {
	serverClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/session-peer/conversation" || r.URL.Query().Get("node") != "peer-7" || r.URL.Query().Get("since") != "41" || r.URL.Query().Get("stream") != "1" {
			http.Error(w, "wrong stream route", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		message := sse.Message{}
		message.AppendData(`{"seq":42,"ts":1,"kind":"prompt","payload":{"text":"hello"}}`)
		_, _ = message.WriteTo(w)
		flusher.Flush()
		<-r.Context().Done()
		close(serverClosed)
	}))
	defer server.Close()

	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	updates, cancel, err := SubscribeConversation(context.Background(), home, "session-peer", "peer-7", 41)
	if err != nil {
		t.Fatal(err)
	}
	if cap(updates) != conversationUpdateBuffer {
		t.Fatalf("stream buffer = %d, want %d", cap(updates), conversationUpdateBuffer)
	}
	update := <-updates
	if update.Err != nil || update.Entry.Seq != 42 || update.Entry.Kind != "prompt" {
		t.Fatalf("stream update = %+v", update)
	}
	cancel()
	<-serverClosed
	if _, ok := <-updates; ok {
		t.Fatal("stream remained open after cancellation")
	}
}

func TestConversationSnapshotIsBoundedAndNodeRouted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/session-peer/conversation" || r.URL.Query().Get("node") != "peer-7" || r.URL.Query().Get("since") != "0" {
			http.Error(w, "wrong snapshot route", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("{\"seq\":1,\"ts\":1,\"kind\":\"prompt\",\"payload\":{}}\n"))
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	entries, truncated, err := ConversationSnapshot(context.Background(), home, "session-peer", "peer-7", 0)
	if err != nil || truncated || len(entries) != 1 || entries[0].Seq != 1 {
		t.Fatalf("snapshot = %+v truncated=%v, err=%v", entries, truncated, err)
	}
}

func TestConversationSnapshotRejectsOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxConversationSnapshotBytes+1)))
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	if _, _, err := ConversationSnapshot(context.Background(), home, "session-peer", "", 0); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize snapshot error = %v", err)
	}
}

func TestSubscribeConversationReportsTruncationAndMalformedUpdates(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		truncated := sse.Message{Type: sse.Type("truncated")}
		_, _ = truncated.WriteTo(w)
		invalid := sse.Message{}
		invalid.AppendData("not-json")
		_, _ = invalid.WriteTo(w)
		flusher.Flush()
		<-r.Context().Done()
		close(serverDone)
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	updates, cancel, err := SubscribeConversation(context.Background(), home, "session-peer", "peer-7", 0)
	if err != nil {
		t.Fatal(err)
	}
	truncated := <-updates
	if !truncated.Truncated || truncated.Err != nil {
		t.Fatalf("truncation update = %+v", truncated)
	}
	invalid := <-updates
	if invalid.Err == nil {
		t.Fatalf("malformed update had no error: %+v", invalid)
	}
	cancel()
	<-serverDone
}

func TestSubscribeConversationReportsSSEFramingError(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		message := sse.Message{}
		message.AppendData(strings.Repeat("x", 70<<10))
		_, _ = message.WriteTo(w)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverDone)
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	updates, cancel, err := SubscribeConversation(context.Background(), home, "session-peer", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if update := <-updates; update.Err == nil {
		t.Fatalf("oversized SSE frame had no error: %+v", update)
	}
	cancel()
	<-serverDone
}

func TestConversationSnapshotKeepsLocalRouteUnlabelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "" {
			http.Error(w, "local request had peer routing", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Agentpack-Truncated", "true")
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubNode(t, home, strings.TrimPrefix(server.URL, "http://"))
	entries, truncated, err := ConversationSnapshot(context.Background(), home, "session-local", "local", 0)
	if err != nil || len(entries) != 0 || !truncated {
		t.Fatalf("local snapshot = %+v truncated=%v, err=%v", entries, truncated, err)
	}
}

func TestConversationSnapshotUsesLocalWorkerWithoutGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversation" || r.URL.Query().Get("since") != "0" {
			http.Error(w, "wrong local worker route", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("{\"seq\":1,\"ts\":1,\"kind\":\"prompt\",\"payload\":{}}\n"))
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubWorker(t, home, "session-local", strings.TrimPrefix(server.URL, "http://"))
	if err := os.WriteFile(filepath.Join(home, "network", "disabled"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := ConversationSnapshot(context.Background(), home, "session-local", "local", 0)
	if err != nil || truncated || len(entries) != 1 || entries[0].Seq != 1 {
		t.Fatalf("local snapshot = %+v truncated=%v, err=%v", entries, truncated, err)
	}
}

func TestSubscribeConversationUsesLocalWorkerWithoutGateway(t *testing.T) {
	serverClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversation" || r.URL.Query().Get("node") != "" || r.URL.Query().Get("stream") != "1" {
			http.Error(w, "wrong local worker stream route", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		message := sse.Message{}
		message.AppendData("{\"seq\":1,\"ts\":1,\"kind\":\"prompt\",\"payload\":{}}")
		_, _ = message.WriteTo(w)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverClosed)
	}))
	defer server.Close()
	home := t.TempDir()
	writeHubWorker(t, home, "session-local", strings.TrimPrefix(server.URL, "http://"))
	if err := os.WriteFile(filepath.Join(home, "network", "disabled"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	updates, cancel, err := SubscribeConversation(context.Background(), home, "session-local", "local", 0)
	if err != nil {
		t.Fatal(err)
	}
	update := <-updates
	if update.Err != nil || update.Entry.Seq != 1 {
		t.Fatalf("local stream update = %+v", update)
	}
	cancel()
	<-serverClosed
}

func writeHubWorker(t *testing.T, home, id, address string) {
	t.Helper()
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(WorkerRecord{ID: id, Address: address, Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
