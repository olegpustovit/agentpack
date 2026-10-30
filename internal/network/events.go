package network

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/tmaxmax/go-sse"

	"github.com/olegpustovit/agentpack/internal/paths"
)

// eventMessage is the JSON payload of one /api/events message. Node is empty for this machine.
type eventMessage struct {
	Topic string `json:"topic"`
	Node  string `json:"node"`
}

// eventTopic is one change source. digest must be stable while the underlying state is unchanged.
type eventTopic struct {
	name     string
	interval time.Duration
	digest   func(context.Context) (string, error)
}

const (
	eventSubscriberBuffer = 64
	eventPingInterval     = 25 * time.Second
	eventNodeInterval     = 15 * time.Second
	eventRelayMaxDelay    = 30 * time.Second
)

var errNoPeerEvents = errors.New("peer does not serve /api/events")

// eventHub fans change notifications out to /api/events streams. Pollers and peer relays run
// only while at least one stream is subscribed. The unexported fields are test seams: nil means
// the real sources.
type eventHub struct {
	mu     sync.Mutex
	subs   map[chan eventMessage]struct{}
	cancel context.CancelFunc

	topics []eventTopic
	nodes  func(context.Context) ([]TailnetNode, error)
	target func(context.Context, string) (*url.URL, error)
	client *http.Client
}

// subscribe returns a stream of events and the function that ends it. The channel is closed
// when a subscriber falls behind, so the client reconnects and receives a fresh baseline.
func (g *Gateway) subscribe() (<-chan eventMessage, func()) {
	h := &g.events
	ch := make(chan eventMessage, eventSubscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[chan eventMessage]struct{}{}
	}
	h.subs[ch] = struct{}{}
	if len(h.subs) == 1 {
		var ctx context.Context
		ctx, h.cancel = context.WithCancel(context.Background())
		g.startEventLoops(ctx)
	}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		g.dropSubscriber(ch)
	}
}

// dropSubscriber requires h.mu. The last subscriber leaving stops every loop.
func (g *Gateway) dropSubscriber(ch chan eventMessage) {
	h := &g.events
	if _, ok := h.subs[ch]; !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
	if len(h.subs) == 0 {
		h.cancel()
		h.cancel = nil
	}
}

func (g *Gateway) emitEvent(msg eventMessage) {
	h := &g.events
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			g.dropSubscriber(ch)
		}
	}
}

func (g *Gateway) startEventLoops(ctx context.Context) {
	h := &g.events
	topics := h.topics
	if topics == nil {
		topics = g.defaultEventTopics()
	}
	for _, topic := range topics {
		go g.pollTopic(ctx, topic)
	}
	go g.pollNodes(ctx)
}

func (g *Gateway) defaultEventTopics() []eventTopic {
	home := g.options.Home
	topics := []eventTopic{
		// shortcut: polls local state every 2 s while a browser is connected, replace with worker push if this shows in profiles
		{name: "sessions", interval: 2 * time.Second, digest: func(ctx context.Context) (string, error) {
			value, err := Sessions(ctx, home)
			return digestOf(value, err)
		}},
		{name: "projects", interval: 10 * time.Second, digest: func(context.Context) (string, error) {
			folders, err := RecentFolders(home)
			if err != nil {
				return "", err
			}
			locks := make([]string, len(folders))
			for i, folder := range folders {
				if info, err := os.Stat(filepath.Join(folder.Directory, paths.LockfileName)); err == nil {
					locks[i] = info.ModTime().UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(info.Size(), 10)
				}
			}
			return digestOf(map[string]any{"folders": folders, "locks": locks}, nil)
		}},
	}
	if store := g.options.Library.Store; store != nil {
		topics = append(topics, eventTopic{name: "library", interval: 10 * time.Second, digest: func(context.Context) (string, error) {
			agentpacks, err := store.ListAgentpacks()
			if err != nil {
				return "", err
			}
			packages, err := store.ListPackages()
			return digestOf(map[string]any{"agentpacks": agentpacks, "packages": packages}, err)
		}})
	}
	return topics
}

func (g *Gateway) pollTopic(ctx context.Context, topic eventTopic) {
	ticker := time.NewTicker(topic.interval)
	defer ticker.Stop()
	last := ""
	for {
		if digest, err := topic.digest(ctx); err == nil && digest != last {
			last = digest
			g.emitEvent(eventMessage{Topic: topic.name})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollNodes emits nodes changes and keeps one relay per answering peer. Each relay forwards only
// the peer's direct events, relabelled with the peer ID; events the peer already labelled are
// dropped so two gateways never echo each other.
func (g *Gateway) pollNodes(ctx context.Context) {
	h := &g.events
	relays := map[string]context.CancelFunc{}
	defer func() {
		for _, cancel := range relays {
			cancel()
		}
	}()
	list := h.nodes
	if list == nil {
		list = func(ctx context.Context) ([]TailnetNode, error) { return TailnetNodes(ctx, g.options.Home) }
	}
	target := h.target
	if target == nil {
		target = func(ctx context.Context, id string) (*url.URL, error) { return tailnetTarget(ctx, g.options.Home, id) }
	}
	client := h.client
	if client == nil {
		client = &http.Client{Transport: tailnetTransport}
	}
	ticker := time.NewTicker(eventNodeInterval)
	defer ticker.Stop()
	last := ""
	for {
		if nodes, err := list(ctx); err == nil {
			if digest, err := digestOf(nodes, nil); err == nil && digest != last {
				last = digest
				g.emitEvent(eventMessage{Topic: "nodes"})
			}
			present := map[string]bool{}
			for _, node := range nodes {
				present[node.ID] = true
				if _, ok := relays[node.ID]; ok {
					continue
				}
				peer, err := target(ctx, node.ID)
				if err != nil {
					continue
				}
				relayCtx, cancel := context.WithCancel(ctx)
				relays[node.ID] = cancel
				go g.relayPeer(relayCtx, client, node.ID, peer)
			}
			for id, cancel := range relays {
				if !present[id] {
					cancel()
					delete(relays, id)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// relayPeer keeps one upstream /api/events connection to a peer and re-emits its direct events.
// A peer answering 404 predates this endpoint, so the relay stops until the peer leaves and returns.
func (g *Gateway) relayPeer(ctx context.Context, client *http.Client, node string, target *url.URL) {
	delay := time.Second
	for {
		connected, err := g.readPeerEvents(ctx, client, node, target)
		if errors.Is(err, errNoPeerEvents) {
			return
		}
		if connected {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, eventRelayMaxDelay)
	}
}

func (g *Gateway) readPeerEvents(ctx context.Context, client *http.Client, node string, target *url.URL) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath("/api/events").String(), nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return false, errNoPeerEvents
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("peer events: %s", response.Status)
	}
	for event, err := range sse.Read(response.Body, nil) {
		if err != nil {
			return true, err
		}
		var msg eventMessage
		if json.Unmarshal([]byte(event.Data), &msg) != nil || msg.Node != "" {
			continue
		}
		msg.Node = node
		g.emitEvent(msg)
	}
	return true, io.ErrUnexpectedEOF
}

// digestOf hashes a JSON encoding so change detection compares content, not object identity.
func digestOf(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// serveEvents serves GET /api/events as a server-sent event stream.
func (g *Gateway) serveEvents(w http.ResponseWriter, r *http.Request) {
	private := r.Header.Get("Origin") == "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.local.Secret)) == 1
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, unsubscribe := g.subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ping := time.NewTicker(eventPingInterval)
	defer ping.Stop()
	auth := time.NewTicker(time.Second)
	defer auth.Stop()
	for {
		var message sse.Message
		select {
		case <-r.Context().Done():
			return
		case <-g.done:
			return
		case <-auth.C:
			if !private && !g.auth.authenticated(r) {
				return
			}
			continue
		case <-ping.C:
			message.AppendComment("ping")
		case msg, open := <-ch:
			if !open {
				return
			}
			data, _ := json.Marshal(msg)
			message.AppendData(string(data))
		}
		if _, err := message.WriteTo(w); err != nil {
			return
		}
		flusher.Flush()
	}
}
