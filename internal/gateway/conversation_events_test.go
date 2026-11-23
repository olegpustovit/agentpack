package gateway

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

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

// The gateway relays the worker's derived frame to the browser beside its entries.
func TestConversationRelayForwardsDerivedFrames(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	id := "session-relay"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 1\ndata: {\"seq\":1,\"kind\":\"prompt\",\"payload\":{}}\n\n" +
			"event: derived\ndata: {\"conversation_state\":\"working\",\"working\":true,\"needs_you\":false,\"through\":1}\n\n"))
	}))
	defer worker.Close()
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := network.PublishWorkerEndpoint(directory, network.WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: strings.Repeat("s", 64)}); err != nil {
		t.Fatal(err)
	}
	var frames []conversationStreamFrame
	since, live, err := g.readConversationStream(t.Context(), id, "local", 0, func(frame conversationStreamFrame) bool {
		frames = append(frames, frame)
		return true
	})
	if err != nil || !live || since != 1 || len(frames) != 2 || frames[0].event != "conversation" ||
		frames[1].event != "conversation_derived" || !strings.Contains(string(frames[1].body), `"through":1`) {
		t.Fatalf("since=%d live=%v err=%v frames=%+v", since, live, err, frames)
	}
}

// An offline chat tab rereads the stored chat only on a state change or the recheck, never on a timer.
func TestOfflineConversationFollowWaitsForStateChanges(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	recheck := make(chan time.Time)
	g.events.recheck = recheck
	const id = "session-offline-follow"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	add := func(seq int64) {
		t.Helper()
		entry := acp.LogEntry{Seq: seq, TS: seq, Kind: "user_message", Payload: json.RawMessage(`{"text":"hi"}`)}
		if _, err := g.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, seq, string(network.MustRawJSON(entry))); err != nil {
			t.Fatal(err)
		}
		if _, err := g.store.DB.Exec("UPDATE sessions SET last_seq=? WHERE id=?", seq, id); err != nil {
			t.Fatal(err)
		}
	}
	add(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := g.followConversation(httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events?session="+id+"&owner=local", nil))
	expect := func(want int64) {
		t.Helper()
		if frame := <-frames; frame.event != "conversation" || frame.seq != want {
			t.Fatalf("frame = %+v, want seq %d", frame, want)
		}
	}
	expect(1)
	before := session.SessionRowReads.Load()
	add(2)
	g.emitEvent(eventMessage{Topic: "projects"})
	g.emitEvent(eventMessage{Topic: "state", Tables: []string{"push_subscriptions"}})
	g.emitEvent(eventMessage{Topic: "state", Tables: []string{"conversation_events"}})
	expect(2)
	if reads := session.SessionRowReads.Load() - before; reads != 1 {
		t.Fatalf("offline follow read the session %d times for one relevant change", reads)
	}
	add(3)
	recheck <- time.Now()
	expect(3)
}
