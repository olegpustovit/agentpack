package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tmaxmax/go-sse"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

func eventsGateway(topics []eventTopic, nodes func(context.Context) ([]mesh.Node, error), target func(context.Context, string) (*url.URL, error)) *Gateway {
	if topics == nil {
		topics = []eventTopic{}
	}
	if nodes == nil {
		nodes = func(context.Context) ([]mesh.Node, error) { return nil, nil }
	}
	g := &Gateway{local: network.NodeRecord{Secret: strings.Repeat("x", 64)}}
	g.events.topics = topics
	g.events.nodes = nodes
	g.events.target = target
	return g
}

func collectEvents(ch <-chan eventMessage, window time.Duration) []eventMessage {
	var out []eventMessage
	timer := time.After(window)
	for {
		select {
		case msg, open := <-ch:
			if !open {
				return out
			}
			out = append(out, msg)
		case <-timer:
			return out
		}
	}
}

func topicCount(messages []eventMessage, topic string) int {
	count := 0
	for _, msg := range messages {
		if msg.Topic == topic {
			count++
		}
	}
	return count
}

func TestSessionsTopicProbesOnlyTerminalWorkers(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	var chatProbes, terminalProbes atomic.Int32
	var running atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer a") {
			chatProbes.Add(1)
		} else {
			terminalProbes.Add(1)
		}
		network.WriteJSON(w, session.TerminalState{Running: running.Load()})
	}))
	defer worker.Close()
	for _, transport := range []string{"acp", "shell"} {
		id := "session-" + transport
		if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: transport}); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(home, "network", "sessions", id)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		record := network.WorkerRecord{ID: id, Address: worker.Listener.Addr().String(), Secret: strings.Repeat(transport[:1], 64)}
		if err := network.PublishWorkerEndpoint(directory, record); err != nil {
			t.Fatal(err)
		}
	}
	digest := g.defaultEventTopics()[1].digest
	before, err1 := digest(t.Context())
	running.Store(true)
	after, err2 := digest(t.Context())
	if err1 != nil || err2 != nil || before == after || chatProbes.Load() != 0 || terminalProbes.Load() != 2 {
		t.Fatalf("terminal digest changed=%v, probes (chat=%d, terminal=%d), errors %v, %v",
			before != after, chatProbes.Load(), terminalProbes.Load(), err1, err2)
	}
}

func TestEventsEmitOnlyOnChange(t *testing.T) {
	var state atomic.Int64
	topic := eventTopic{name: "sessions", interval: 5 * time.Millisecond, digest: func(context.Context) (string, error) {
		return strconv.FormatInt(state.Load(), 10), nil
	}}
	g := eventsGateway([]eventTopic{topic}, nil, nil)
	ch, unsubscribe := g.subscribe()
	defer unsubscribe()
	if n := topicCount(collectEvents(ch, 60*time.Millisecond), "sessions"); n != 1 {
		t.Fatalf("baseline emitted %d sessions events, want 1", n)
	}
	if n := topicCount(collectEvents(ch, 60*time.Millisecond), "sessions"); n != 0 {
		t.Fatalf("unchanged state emitted %d sessions events", n)
	}
	state.Store(1)
	if n := topicCount(collectEvents(ch, 60*time.Millisecond), "sessions"); n != 1 {
		t.Fatalf("change emitted %d sessions events, want 1", n)
	}
}

func TestEventsKeepPollersAcrossShortSubscriberGaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls, nodePolls atomic.Int32
		ticks := make(chan time.Time, 1)
		topic := eventTopic{name: "state", tick: ticks, digest: func(context.Context) (string, error) {
			return strconv.Itoa(int(polls.Add(1))), nil
		}, detail: func(context.Context, string, string) []string { return []string{"users"} }}
		g := eventsGateway([]eventTopic{topic}, func(context.Context) ([]mesh.Node, error) {
			nodePolls.Add(1)
			return nil, nil
		}, nil)
		_, unsubscribe := g.subscribe()
		synctest.Wait()
		unsubscribe()
		time.Sleep(eventIdleGrace - time.Second)
		reopened, stop := g.subscribe()
		defer func() {
			stop()
			time.Sleep(eventIdleGrace)
			synctest.Wait()
		}()
		synctest.Wait()
		if polls.Load() != 1 || nodePolls.Load() != 1 {
			t.Fatalf("reopening restarted pollers: state=%d nodes=%d", polls.Load(), nodePolls.Load())
		}
		time.Sleep(time.Second) // the original grace deadline
		ticks <- time.Time{}
		synctest.Wait()
		if polls.Load() != 2 || len(reopened) != 1 {
			t.Fatal("the returning subscriber did not receive the next state change")
		}
		if got := <-reopened; got.Topic != "state" || !reflect.DeepEqual(got.Tables, []string{"users"}) {
			t.Fatalf("state event lost its baseline: %+v", got)
		}
		stop()
		time.Sleep(eventIdleGrace)
		synctest.Wait()
		ticks <- time.Time{}
		synctest.Wait()
		if polls.Load() != 2 {
			t.Fatalf("polled %d times after the final grace elapsed", polls.Load())
		}
	})
}

func TestEventsRelayPeerDirectEventsOnly(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"topic\":\"sessions\",\"node\":\"\"}\n\n")
		fmt.Fprint(w, "data: {\"topic\":\"library\",\"node\":\"other\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer peer.Close()
	target := func(context.Context, string) (*url.URL, error) { return url.Parse(peer.URL) }
	nodes := func(context.Context) ([]mesh.Node, error) { return []mesh.Node{{ID: "peer1", Name: "peer1"}}, nil }
	g := eventsGateway(nil, nodes, target)
	shutdown := make(chan struct{})
	g.done = shutdown
	defer close(shutdown)
	ch, unsubscribe := g.subscribe()
	defer unsubscribe()
	messages := collectEvents(ch, 300*time.Millisecond)
	var relayed []eventMessage
	for _, msg := range messages {
		if msg.Topic == "sessions" || msg.Topic == "library" {
			relayed = append(relayed, msg)
		}
	}
	if len(relayed) != 1 || !reflect.DeepEqual(relayed[0], eventMessage{Topic: "sessions", Node: "peer1"}) {
		t.Fatalf("relayed events = %+v, want exactly sessions from peer1", relayed)
	}
}

func TestEventsSkipPeerWithoutEndpoint(t *testing.T) {
	var hits atomic.Int64
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer peer.Close()
	target := func(context.Context, string) (*url.URL, error) { return url.Parse(peer.URL) }
	nodes := func(context.Context) ([]mesh.Node, error) { return []mesh.Node{{ID: "old", Name: "old"}}, nil }
	g := eventsGateway(nil, nodes, target)
	ch, unsubscribe := g.subscribe()
	defer unsubscribe()
	collectEvents(ch, 200*time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Fatalf("peer without /api/events was contacted %d times, want 1", n)
	}
}

func TestEventsStreamFraming(t *testing.T) {
	g := eventsGateway([]eventTopic{{name: "sessions", interval: time.Hour, digest: func(context.Context) (string, error) {
		return "one", nil
	}}}, nil, nil)
	server := httptest.NewServer(http.HandlerFunc(g.serveEvents))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/events", nil)
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if ct := response.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	if cc := response.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache control %q", cc)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == `data: {"topic":"sessions","node":""}` {
			return
		}
	}
	t.Fatal("stream never carried the sessions event", scanner.Err())
}

func TestEventsStreamClosesOnLogout(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "agentpack_session", Value: network.RandomToken()}
	testBrowserSession(t, auth, testBrowserEmail, cookie.Value)

	g := eventsGateway(nil, nil, nil)
	g.store, g.auth = auth.store, auth
	g.events.checkAuth = make(chan time.Time)
	server := httptest.NewServer(http.HandlerFunc(g.serveEvents))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/events", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	logout := httptest.NewRequest("POST", "https://agentpack.example/api/logout", nil)
	logout.AddCookie(cookie)
	g.logout(httptest.NewRecorder(), logout)

	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, response.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream stayed open after logout")
	}
}

func TestInternalListenersDoNotKeepPollersRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls atomic.Int32
		topic := eventTopic{name: "sessions", interval: time.Second, digest: func(context.Context) (string, error) {
			polls.Add(1)
			return "", nil
		}}
		g := eventsGateway([]eventTopic{topic}, nil, nil)
		heard, stopListening := g.listen()
		defer stopListening()
		synctest.Wait()
		if polls.Load() != 0 {
			t.Fatal("an internal listener started the pollers")
		}
		_, unsubscribe := g.subscribe()
		synctest.Wait()
		g.emitEvent(eventMessage{Topic: "replication"})
		for msg := range heard {
			if msg.Topic == "replication" {
				break
			}
		}
		unsubscribe()
		time.Sleep(eventIdleGrace)
		synctest.Wait()
		settled := polls.Load()
		time.Sleep(time.Second)
		synctest.Wait()
		if polls.Load() != settled {
			t.Fatal("pollers kept running for an internal listener after the last stream left")
		}
	})
}

func TestStateEventsNameTheTablesThatChanged(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth}
	before, err := auth.store.DBVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.UpsertSession(t.Context(), auth.store.DB, session.SessionInfo{ID: "session-tables"}); err != nil {
		t.Fatal(err)
	}
	after, err := auth.store.DBVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tables := g.changedTables(t.Context(), strconv.FormatInt(before, 10), strconv.FormatInt(after, 10))
	if len(tables) != 1 || tables[0] != "sessions" {
		t.Fatalf("changed tables %v between %d and %d", tables, before, after)
	}
	if g.changedTables(t.Context(), "", "5") != nil {
		t.Fatal("an unknown baseline must report unknown tables")
	}
}

func TestStreamingProgressOnASessionRowIsNotASessionsChange(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth}
	if err := session.UpsertSession(t.Context(), auth.store.DB, session.SessionInfo{ID: "session-stream"}); err != nil {
		t.Fatal(err)
	}
	before, _ := auth.store.DBVersion(t.Context())
	if err := network.PersistConversationChangesSince(t.Context(), auth.store.DB, "session-stream", acp.LogChanges{LastSeq: 1, Entries: []acp.LogEntry{{Seq: 1, Kind: "agent_message_chunk", Payload: json.RawMessage(`{}`)}}}, 0, false); err != nil {
		t.Fatal(err)
	}
	after, _ := auth.store.DBVersion(t.Context())
	if tables := g.changedTables(t.Context(), strconv.FormatInt(before, 10), strconv.FormatInt(after, 10)); !reflect.DeepEqual(tables, []string{"conversation_events"}) {
		t.Fatalf("a streamed chunk reported %v", tables)
	}
}

func TestStatePollSkipsStreamingOnlyChangesAndAdvances(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var versions []string
	ticks := make(chan time.Time, 3)
	for _, statement := range []string{
		"INSERT INTO sessions(id) VALUES ('session-progress')",
		"UPDATE sessions SET last_seq=1,last_event_at=1,conversation_truncated=1 WHERE id='session-progress'",
		"UPDATE sessions SET conversation_state='working' WHERE id='session-progress'",
	} {
		if _, err := g.store.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
		version, err := g.store.DBVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, strconv.FormatInt(version, 10))
		ticks <- time.Time{}
	}
	topic := g.defaultEventTopics()[0]
	topic.tick = ticks
	var next int
	topic.digest = func(context.Context) (string, error) {
		if next == len(versions) {
			cancel()
			return "", ctx.Err()
		}
		version := versions[next]
		next++
		return version, nil
	}
	detail := topic.detail
	topic.detail = func(ctx context.Context, previous, current string) []string {
		if current == versions[2] && previous != versions[1] {
			t.Errorf("last version = %s, want %s", previous, versions[1])
		}
		return detail(ctx, previous, current)
	}
	events, unsubscribe := g.listen()
	g.pollTopic(ctx, topic)
	unsubscribe()
	var got []eventMessage
	for msg := range events {
		got = append(got, msg)
	}
	want := []eventMessage{{Topic: "state"}, {Topic: "state", Tables: []string{"sessions"}}, {Topic: "sessions"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

func TestEventsCarriesConversationFromItsOwnResumeCursor(t *testing.T) {
	g, _ := identityGateway(t, t.TempDir(), nil)
	g.events.topics = []eventTopic{}
	g.events.nodes = func(context.Context) ([]mesh.Node, error) { return nil, nil }
	const id = "session-multiplexed"
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp", ConversationState: "starting"}); err != nil {
		t.Fatal(err)
	}
	entries := []acp.LogEntry{
		{Seq: 1, Kind: "queued_prompt", Payload: json.RawMessage(`{}`)},
		{Seq: 2, Kind: "queued_prompt", Payload: json.RawMessage(`{}`)},
	}
	if err := network.PersistConversationChangesSince(t.Context(), g.store.DB, id, acp.LogChanges{Entries: entries, LastSeq: 2}, 0, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(g.serveEvents))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events?owner=local&session="+id+"&since=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	request.Header.Set("Last-Event-ID", id+":1")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	g.emitEvent(eventMessage{Topic: "sessions"})
	var receivedConversation, receivedState bool
	for event, err := range sse.Read(response.Body, nil) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == "conversation" {
			if event.LastEventID != id+":2" || !strings.Contains(event.Data, `"seq":2`) {
				t.Fatalf("conversation did not resume at sequence 2: %+v", event)
			}
			receivedConversation = true
		}
		if event.Type == "" && strings.Contains(event.Data, `"topic":"sessions"`) {
			receivedState = true
		}
		if receivedConversation && receivedState {
			return
		}
	}
	t.Fatal("one stream did not carry both event types")
}

func TestSessionTableChangesEmitSessionsWithoutWorkerPoll(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := eventsGateway(nil, nil, nil)
	g.store, g.auth = auth.store, auth
	ticks := make(chan time.Time, 1)
	topic := g.defaultEventTopics()[0]
	topic.tick = ticks
	events, unsubscribe := g.listen()
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go g.pollTopic(ctx, topic)
	next := func() eventMessage {
		t.Helper()
		select {
		case msg := <-events:
			return msg
		case <-ctx.Done():
			t.Fatal("timed out waiting for event")
			return eventMessage{}
		}
	}
	if got := next(); got.Topic != "state" {
		t.Fatalf("initial event = %+v", got)
	}
	if err := session.UpsertSession(ctx, auth.store.DB, session.SessionInfo{ID: "session-changed"}); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	if got := next(); got.Topic != "state" || !reflect.DeepEqual(got.Tables, []string{"sessions"}) {
		t.Fatalf("state event = %+v", got)
	}
	if got := next(); got.Topic != "sessions" {
		t.Fatalf("sessions event = %+v", got)
	}
}

// A peer's relay runs this machine's pollers but never its own relays, so two gateways with no
// browser open cannot keep each other's loops alive.
func TestPeerSubscriberDoesNotRelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls, nodePolls atomic.Int32
		topic := eventTopic{name: "sessions", interval: time.Second, digest: func(context.Context) (string, error) {
			polls.Add(1)
			return "steady", nil
		}}
		g := eventsGateway([]eventTopic{topic}, func(context.Context) ([]mesh.Node, error) {
			nodePolls.Add(1)
			return nil, nil
		}, nil)
		_, unsubscribePeer := g.subscribeEvents(peerSubscriber)
		defer func() {
			unsubscribePeer()
			time.Sleep(eventIdleGrace)
			synctest.Wait()
		}()
		synctest.Wait()
		if polls.Load() != 1 || nodePolls.Load() != 0 {
			t.Fatalf("peer subscriber started %d polls and %d node polls, want 1 and 0", polls.Load(), nodePolls.Load())
		}
		_, unsubscribeBrowser := g.subscribe()
		defer unsubscribeBrowser()
		synctest.Wait()
		if nodePolls.Load() != 1 {
			t.Fatal("the browser did not start relays")
		}
		unsubscribeBrowser()
		time.Sleep(eventNodeInterval)
		synctest.Wait()
		if nodePolls.Load() != 1 || polls.Load() <= 1 {
			t.Fatalf("browser leaving must stop relays only: node polls=%d polls=%d", nodePolls.Load(), polls.Load())
		}
	})
}
