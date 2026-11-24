package gateway

import (
	"context"
	"crypto/sha256"
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

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
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
	tick     <-chan time.Time
	digest   func(context.Context) (string, error)
	// detail returns nil for unknown changes and an empty slice for changes to skip.
	detail func(ctx context.Context, previous, current string) []string
}

const (
	eventSubscriberBuffer = 64
	eventIdleGrace        = 5 * time.Second
	eventPingInterval     = 25 * time.Second
	eventNodeInterval     = 15 * time.Second
	eventRelayMaxDelay    = 30 * time.Second
)

var errNoPeerEvents = errors.New("peer does not serve /api/events")

// eventHub fans change notifications out to /api/events streams. Pollers and peer relays
// outlast a short gap between streams so a chat switch keeps their baselines.
type eventHub struct {
	mu       sync.Mutex
	subs     map[chan eventMessage]subscriber
	watchers eventWatch
	// A peer's relay never keeps our relays alive, so two gateways cannot keep each other watching.
	browsers eventWatch

	topics    []eventTopic
	nodes     func(context.Context) ([]mesh.Node, error)
	target    func(context.Context, string) (*url.URL, error)
	client    *http.Client
	checkAuth <-chan time.Time
	recheck   <-chan time.Time // replaces conversationRecheck in tests
}

type eventWatch struct {
	count  int
	cancel context.CancelFunc
	idle   *time.Timer
}

// subscribe returns a stream of events and the function that ends it. The channel is closed
// when a subscriber falls behind, so the client reconnects and receives a fresh baseline.
func (g *Gateway) subscribe() (<-chan eventMessage, func()) {
	return g.subscribeEvents(browserSubscriber)
}

// listen hears emitted events without starting the pollers, so background work never keeps them running.
func (g *Gateway) listen() (<-chan eventMessage, func()) {
	return g.subscribeEvents(listenSubscriber)
}

type subscriber int

const (
	listenSubscriber  subscriber = iota // hears events only
	peerSubscriber                      // runs this machine's pollers for a peer's relay
	browserSubscriber                   // also relays every peer's events
)

func (g *Gateway) subscribeEvents(kind subscriber) (<-chan eventMessage, func()) {
	h := &g.events
	ch := make(chan eventMessage, eventSubscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[chan eventMessage]subscriber{}
	}
	h.subs[ch] = kind
	if kind >= peerSubscriber {
		g.addEventWatcher(&h.watchers, g.startEventLoops)
	}
	if kind == browserSubscriber {
		g.addEventWatcher(&h.browsers, func(ctx context.Context) {
			obs.Go("gateway.events.nodes", func() { g.pollNodes(ctx) })
		})
	}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		g.dropSubscriber(ch)
	}
}

// dropSubscriber requires h.mu.
func (g *Gateway) dropSubscriber(ch chan eventMessage) {
	h := &g.events
	kind, ok := h.subs[ch]
	if !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
	if kind == browserSubscriber {
		g.dropEventWatcher(&h.browsers)
	}
	if kind >= peerSubscriber {
		g.dropEventWatcher(&h.watchers)
	}
}

// addEventWatcher and dropEventWatcher are called with the hub locked.
func (g *Gateway) addEventWatcher(watch *eventWatch, start func(context.Context)) {
	watch.count++
	if watch.idle != nil {
		watch.idle.Stop()
		watch.idle = nil
	}
	if watch.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	watch.cancel = cancel
	if g.done != nil {
		go func() {
			select {
			case <-g.done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	start(ctx)
}

func (g *Gateway) dropEventWatcher(watch *eventWatch) {
	watch.count--
	if watch.count != 0 {
		return
	}
	var idle *time.Timer
	idle = time.AfterFunc(eventIdleGrace, func() {
		g.events.mu.Lock()
		defer g.events.mu.Unlock()
		// A returning watcher can supersede a callback already waiting for the lock.
		if watch.idle == idle {
			watch.cancel()
			watch.cancel, watch.idle = nil, nil
		}
	})
	watch.idle = idle
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
}

func (g *Gateway) defaultEventTopics() []eventTopic {
	home := g.options.Home
	topics := []eventTopic{
		{name: "state", interval: 2 * time.Second, digest: g.stateDigest, detail: g.changedTables},
		// The fallback catches terminal-only state that workers do not persist.
		{name: "sessions", interval: 30 * time.Second, digest: func(ctx context.Context) (string, error) {
			entries, err := session.QuerySessions(ctx, g.store.Reader, "SELECT "+session.SessionColumns+
				" FROM sessions WHERE node_id="+state.LocalNodeSQL+" AND ended_at=0 AND transport<>'acp'")
			if err != nil {
				return "", err
			}
			return digestOf(network.WorkerStates(ctx, home, entries), nil)
		}},
		{name: "projects", interval: 10 * time.Second, digest: func(ctx context.Context) (string, error) {
			recent, err := network.RecentIn(ctx, g.store.Reader)
			if err != nil {
				return "", err
			}
			folders := network.RecentFolders(recent)
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

func (g *Gateway) stateDigest(ctx context.Context) (string, error) {
	version, err := g.store.DBVersion(ctx)
	return strconv.FormatInt(version, 10), err
}

var streamingColumns = map[string]bool{"last_seq": true, "last_event_at": true, "conversation_truncated": true}

// changedTables returns nil for an unknown change and an empty slice for streaming-only columns.
func (g *Gateway) changedTables(ctx context.Context, previous, current string) []string {
	from, err1 := strconv.ParseInt(previous, 10, 64)
	to, err2 := strconv.ParseInt(current, 10, 64)
	if err1 != nil || err2 != nil || to <= from {
		return nil
	}
	rows, err := g.store.Reader.QueryContext(ctx, `SELECT DISTINCT "table",cid FROM crsql_changes WHERE db_version>? AND db_version<=?`, from, to)
	if err != nil {
		return nil
	}
	defer rows.Close()
	tables := []string{}
	seen := false
	for rows.Next() {
		var table, column string
		if rows.Scan(&table, &column) != nil {
			return nil
		}
		seen = true
		// A streaming chat bumps these on its session row each flush; no list shows them.
		if table == "sessions" && streamingColumns[column] || slices.Contains(tables, table) {
			continue
		}
		tables = append(tables, table)
	}
	if rows.Err() != nil || !seen {
		return nil
	}
	return tables
}

func (g *Gateway) pollTopic(ctx context.Context, topic eventTopic) {
	tick := topic.tick
	if tick == nil {
		ticker := time.NewTicker(topic.interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	last := ""
	for {
		if digest, err := topic.digest(ctx); err == nil && digest != last {
			message := eventMessage{Topic: topic.name}
			if topic.detail != nil && last != "" {
				message.Tables = topic.detail(ctx, last, digest)
			}
			last = digest
			if message.Tables == nil || len(message.Tables) != 0 {
				g.emitEvent(message)
				if message.Topic == "state" && slices.Contains(message.Tables, "sessions") {
					g.emitEvent(eventMessage{Topic: "sessions"})
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
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
		client = &http.Client{Transport: mesh.Transport}
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
	if g.peer(node).Mesh {
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
func (g *Gateway) registerEvents(mux routeRegistrar) {
	mux.HandleFunc("GET /api/events", g.serveEvents)
}

func (g *Gateway) serveEvents(w http.ResponseWriter, r *http.Request) {
	private := g.localRequest(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	kind := browserSubscriber
	if peer, _ := r.Context().Value(replicaPeerKey{}).(string); peer != "" {
		kind = peerSubscriber
	}
	ch, unsubscribe := g.subscribeEvents(kind)
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
		auth := time.NewTicker(30 * time.Second)
		defer auth.Stop()
		checkAuth = auth.C
	}
	allowed := func() bool {
		peer, _ := r.Context().Value(replicaPeerKey{}).(string)
		if peer != "" {
			if r.URL.Path == "/api/sync/events" {
				return g.peer(peer).Mesh
			}
			return g.peer(peer).Node != ""
		}
		return private || g.auth.authenticated(r)
	}
	for {
		var message sse.Message
		select {
		case <-r.Context().Done():
			return
		case <-g.done:
			return
		case <-checkAuth:
			if !allowed() {
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
			if authChanged(msg) && !allowed() {
				return
			}
			if msg.Topic == "auth" {
				continue
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

func authChanged(event eventMessage) bool {
	return event.Node == "" && (event.Topic == "auth" ||
		event.Topic == "state" && (slices.Contains(event.Tables, "users") || slices.Contains(event.Tables, "auth_sessions")))
}
