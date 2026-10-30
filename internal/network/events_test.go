package network

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func eventsGateway(topics []eventTopic, nodes func(context.Context) ([]TailnetNode, error), target func(context.Context, string) (*url.URL, error)) *Gateway {
	if topics == nil {
		topics = []eventTopic{}
	}
	if nodes == nil {
		nodes = func(context.Context) ([]TailnetNode, error) { return nil, nil }
	}
	g := &Gateway{local: nodeRecord{Secret: strings.Repeat("x", 64)}}
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

func TestEventsRunOnlyWhileSubscribed(t *testing.T) {
	var polls atomic.Int64
	topic := eventTopic{name: "sessions", interval: 2 * time.Millisecond, digest: func(context.Context) (string, error) {
		polls.Add(1)
		return "steady", nil
	}}
	g := eventsGateway([]eventTopic{topic}, nil, nil)
	time.Sleep(30 * time.Millisecond)
	if n := polls.Load(); n != 0 {
		t.Fatalf("polled %d times with no subscriber", n)
	}
	_, unsubscribe := g.subscribe()
	deadline := time.Now().Add(time.Second)
	for polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if polls.Load() == 0 {
		t.Fatal("subscriber did not start polling")
	}
	unsubscribe()
	time.Sleep(30 * time.Millisecond)
	stopped := polls.Load()
	time.Sleep(50 * time.Millisecond)
	if n := polls.Load(); n != stopped {
		t.Fatalf("polling continued after last unsubscribe: %d -> %d", stopped, n)
	}
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
	nodes := func(context.Context) ([]TailnetNode, error) { return []TailnetNode{{ID: "peer1", Name: "peer1"}}, nil }
	g := eventsGateway(nil, nodes, target)
	ch, unsubscribe := g.subscribe()
	defer unsubscribe()
	messages := collectEvents(ch, 300*time.Millisecond)
	var relayed []eventMessage
	for _, msg := range messages {
		if msg.Topic == "sessions" || msg.Topic == "library" {
			relayed = append(relayed, msg)
		}
	}
	if len(relayed) != 1 || relayed[0] != (eventMessage{Topic: "sessions", Node: "peer1"}) {
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
	nodes := func(context.Context) ([]TailnetNode, error) { return []TailnetNode{{ID: "old", Name: "old"}}, nil }
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
	auth, err := newGatewayAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setup := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"password":"a-long-password","token":"` + auth.setup + `"}`)
	auth.login(setup, httptest.NewRequest("POST", "https://agentpack.example/api/setup", body), true, true)
	cookie := setup.Result().Cookies()[0]

	g := eventsGateway(nil, nil, nil)
	g.auth = auth
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
	auth.logout(httptest.NewRecorder(), logout)

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
