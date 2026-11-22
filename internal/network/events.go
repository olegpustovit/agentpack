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
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/tmaxmax/go-sse"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// eventMessage is the JSON payload of one /api/events message. Node is empty for this machine.
type eventMessage struct {
	Topic string `json:"topic"`
	Node  string `json:"node"`
	// Tables lists what a state change touched, so browsers refetch only what reads them. Absent means unknown.
	Tables []string `json:"tables,omitempty"`
}

// eventTopic is one change source. digest must be stable while the underlying state is unchanged.
type eventTopic struct {
	name     string
	interval time.Duration
	digest   func(context.Context) (string, error)
	// detail, when set, describes the change between two digests.
	detail func(ctx context.Context, previous, current string) []string
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
	mu   sync.Mutex
	subs map[chan eventMessage]bool // true for streams that need the pollers running
	// watchers counts subscribers that keep pollers alive; internal listeners only hear emitted events.
	watchers int
	cancel   context.CancelFunc

	topics    []eventTopic
	nodes     func(context.Context) ([]TailnetNode, error)
	target    func(context.Context, string) (*url.URL, error)
	client    *http.Client
	checkAuth <-chan time.Time
}

// subscribe returns a stream of events and the function that ends it. The channel is closed
// when a subscriber falls behind, so the client reconnects and receives a fresh baseline.
func (g *Gateway) subscribe() (<-chan eventMessage, func()) {
	return g.subscribeEvents(true)
}

// listen hears emitted events without starting the pollers, so background work never keeps them running.
func (g *Gateway) listen() (<-chan eventMessage, func()) {
	return g.subscribeEvents(false)
}

func (g *Gateway) subscribeEvents(watch bool) (<-chan eventMessage, func()) {
	h := &g.events
	ch := make(chan eventMessage, eventSubscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[chan eventMessage]bool{}
	}
	h.subs[ch] = watch
	if !watch {
		return ch, func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			g.dropSubscriber(ch)
		}
	}
	h.watchers++
	if h.watchers == 1 {
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
	watch, ok := h.subs[ch]
	if !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
	if !watch {
		return
	}
	h.watchers--
	if h.watchers == 0 {
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
		obs.Go("gateway.events."+topic.name, func() { g.pollTopic(ctx, topic) })
	}
	obs.Go("gateway.events.nodes", func() { g.pollNodes(ctx) })
}

func (g *Gateway) defaultEventTopics() []eventTopic {
	home := g.options.Home
	topics := []eventTopic{
		{name: "state", interval: 2 * time.Second, digest: func(ctx context.Context) (string, error) {
			version, err := g.auth.store.DBVersion(ctx)
			return strconv.FormatInt(version, 10), err
		}, detail: g.changedTables},
		// shortcut: polls local state every 2 s while a browser is connected, replace with worker push if this shows in profiles
		{name: "sessions", interval: 2 * time.Second, digest: func(ctx context.Context) (string, error) {
			value, err := sessionsIn(ctx, g.auth.store.Reader, home)
			return digestOf(value, err)
		}},
		{name: "projects", interval: 10 * time.Second, digest: func(ctx context.Context) (string, error) {
			recent, err := recentIn(ctx, g.auth.store.Reader)
			if err != nil {
				return "", err
			}
			folders := recentFolders(recent)
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

var streamingColumns = map[string]bool{"last_seq": true, "last_event_at": true, "conversation_truncated": true}

// changedTables names the replicated tables written between two state versions; nil means unknown.
func (g *Gateway) changedTables(ctx context.Context, previous, current string) []string {
	from, err1 := strconv.ParseInt(previous, 10, 64)
	to, err2 := strconv.ParseInt(current, 10, 64)
	if err1 != nil || err2 != nil || to <= from {
		return nil
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT DISTINCT "table",cid FROM crsql_changes WHERE db_version>? AND db_version<=?`, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var table, column string
		if rows.Scan(&table, &column) != nil {
			return nil
		}
		// A streaming chat bumps these on its session row each flush; no list shows them.
		if table == "sessions" && streamingColumns[column] || slices.Contains(tables, table) {
			continue
		}
		tables = append(tables, table)
	}
	if rows.Err() != nil || len(tables) == 0 {
		return nil
	}
	return tables
}

func (g *Gateway) pollTopic(ctx context.Context, topic eventTopic) {
	ticker := time.NewTicker(topic.interval)
	defer ticker.Stop()
	last := ""
	for {
		if digest, err := topic.digest(ctx); err == nil && digest != last {
			message := eventMessage{Topic: topic.name}
			if topic.detail != nil && last != "" {
				message.Tables = topic.detail(ctx, last, digest)
			}
			last = digest
			g.emitEvent(message)
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
		list = g.tailnetNodes
	}
	target := h.target
	if target == nil {
		target = g.tailnetTarget
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
				obs.Go("gateway.events.peer", func() { g.relayPeer(relayCtx, client, node.ID, peer) })
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
	path := "/api/events"
	if g.meshPeer(ctx, node) {
		path = "/api/sync/events"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath(path).String(), nil)
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
		if msg.Topic == "state" {
			g.wakeStatePeer(node)
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
	conversation := g.followConversation(r)
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ping := time.NewTicker(eventPingInterval)
	defer ping.Stop()
	checkAuth := g.events.checkAuth
	if checkAuth == nil {
		auth := time.NewTicker(time.Second)
		defer auth.Stop()
		checkAuth = auth.C
	}
	for {
		var message sse.Message
		select {
		case <-r.Context().Done():
			return
		case <-g.done:
			return
		case <-checkAuth:
			peer, _ := r.Context().Value(replicaPeerKey{}).(string)
			allowed := private
			if peer != "" {
				if r.URL.Path == "/api/sync/events" {
					allowed = g.meshPeer(r.Context(), peer)
				} else {
					allowed = g.enrolledPeer(peer)
				}
			} else if !allowed {
				allowed = g.auth.authenticated(r)
			}
			if !allowed {
				return
			}
			continue
		case <-ping.C:
			message.AppendComment("ping")
		case frame, open := <-conversation:
			if !open {
				conversation = nil
				continue
			}
			message.Type = sse.Type(frame.event)
			if frame.seq > 0 {
				message.ID = sse.ID(r.URL.Query().Get("session") + ":" + strconv.FormatInt(frame.seq, 10))
			}
			if len(frame.body) == 0 {
				message.AppendData("{}")
			} else {
				message.AppendData(string(frame.body))
			}
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
