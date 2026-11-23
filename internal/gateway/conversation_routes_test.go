package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

func TestStoppedConversationMatchesLiveHistoryAndSince(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	id := "session-history"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log, err := network.NewConversationLog(t.Context(), g.store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	log.Append("prompt", 1, json.RawMessage(`{"text":"hello"}`))
	log.Append("agent_message_chunk", 2, json.RawMessage(`{"text":{"text":"reply"}}`))
	log.Append("turn_end", 3, json.RawMessage(`{}`))
	log.MarkTruncated()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	worker := &network.WorkerHTTP{Conversation: network.NewConversationSession(log, nil)}
	mux := http.NewServeMux()
	worker.RegisterConversation(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(network.WorkerRecord{ID: id, Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	request := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, origin.String()+"/api/sessions/"+id+"/conversation"+query, nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	live := request("")
	if live.Code != http.StatusOK {
		t.Fatal(live.Code, live.Body.String())
	}
	server.Close()
	for _, removeRecord := range []bool{false, true} {
		if removeRecord {
			if err := os.RemoveAll(directory); err != nil {
				t.Fatal(err)
			}
		}
		stopped := request("")
		if stopped.Code != http.StatusOK || stopped.Body.String() != live.Body.String() || stopped.Header().Get("Content-Type") != live.Header().Get("Content-Type") || stopped.Header().Get("X-Agentpack-Truncated") != "true" {
			t.Fatalf("stopped history differs: status=%d body=%q", stopped.Code, stopped.Body.String())
		}
	}
	filtered := request("?since=1")
	entries, err := network.ReadConversationNDJSON(filtered.Body)
	if err != nil || len(entries) != 2 || entries[0].Seq != 2 || entries[1].Seq != 3 {
		t.Fatalf("stopped since filter: %+v %v", entries, err)
	}
	stream := request("?since=1&stream=1")
	if stream.Code != http.StatusOK || stream.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(stream.Body.String(), `"seq":2`) || !strings.Contains(stream.Body.String(), "event: truncated") {
		t.Fatalf("stopped stream shape: %d %s", stream.Code, stream.Body.String())
	}
}

func TestConversationPagesReachTheFirstEventWithoutTheWorker(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	id := "session-long"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"text": strings.Repeat("x", 200<<10)})
	if err != nil {
		t.Fatal(err)
	}
	const total = 60 // about 12 MiB: three times the tail
	changes := acp.LogChanges{LastSeq: total}
	for seq := int64(1); seq <= total; seq++ {
		changes.Entries = append(changes.Entries, acp.LogEntry{Seq: seq, Kind: "agent_message_chunk", Payload: payload})
	}
	if err := network.PersistConversationChangesSince(t.Context(), g.store.DB, id, changes, 0, false); err != nil {
		t.Fatal(err)
	}
	// A running worker that cannot serve history proves older pages come from the store.
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "tail only", http.StatusTeapot) }))
	defer worker.Close()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(network.WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), record, 0600); err != nil {
		t.Fatal(err)
	}
	page := func(before int64) ([]acp.LogEntry, bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/sessions/%s/conversation?before=%d", origin, id, before), nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("page before %d: %d %s", before, w.Code, w.Body.String())
		}
		entries, err := network.ReadConversationNDJSON(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		return entries, w.Header().Get("X-Agentpack-Truncated") == "true"
	}
	tail, _, truncated, err := network.StoredConversationTail(t.Context(), g.store.Reader, id, 0)
	if err != nil || !truncated || tail[0].Seq == 1 {
		t.Fatalf("tail should stop short of the first event: first=%d truncated=%t err=%v", tail[0].Seq, truncated, err)
	}
	seen := map[int64]bool{}
	for _, entry := range tail {
		seen[entry.Seq] = true
	}
	before, pages := tail[0].Seq, 0
	for more := true; more; pages++ {
		var entries []acp.LogEntry
		entries, more = page(before)
		if len(entries) == 0 || entries[len(entries)-1].Seq != before-1 {
			t.Fatalf("page before %d must end right below it: %+v", before, len(entries))
		}
		for i, entry := range entries {
			if seen[entry.Seq] || i > 0 && entry.Seq != entries[i-1].Seq+1 {
				t.Fatalf("page before %d is not a gapless ascending run: seq %d", before, entry.Seq)
			}
			seen[entry.Seq] = true
		}
		before = entries[0].Seq
	}
	if len(seen) != total || before != 1 || pages < 2 {
		t.Fatalf("paging reached %d of %d events in %d pages, ending at %d", len(seen), total, pages, before)
	}
	if entries, more := page(1); len(entries) != 0 || more {
		t.Fatalf("nothing is older than the first event: %d %t", len(entries), more)
	}
}
