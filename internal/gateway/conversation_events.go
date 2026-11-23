package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/tmaxmax/go-sse"
)

type conversationStreamFrame struct {
	event string
	seq   int64
	body  json.RawMessage
}

var errConversationStreamUnsupported = errors.New("owning gateway does not support the conversation stream")

// The open tab follows the owner from the newest sequence it already has in its local snapshot.
func (g *Gateway) followConversation(r *http.Request) <-chan conversationStreamFrame {
	id, node := r.URL.Query().Get("session"), r.URL.Query().Get("owner")
	if r.URL.Path != "/api/events" || !network.ValidSessionID(id) || len(id) > 128 {
		return nil
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if last, ok := strings.CutPrefix(r.Header.Get("Last-Event-ID"), id+":"); ok {
		seq, _ := strconv.ParseInt(last, 10, 64)
		since = max(since, seq)
	}
	ctx := r.Context()
	frames := make(chan conversationStreamFrame)
	send := func(frame conversationStreamFrame) bool {
		select {
		case frames <- frame:
			return true
		case <-ctx.Done():
			return false
		}
	}
	obs.Go("gateway.follow_conversation", func() {
		defer close(frames)
		// Subscribed before the first read, so a change made while reading still wakes the wait.
		changes, stop := g.listen()
		defer func() { stop() }()
		for ctx.Err() == nil {
			next, live, err := g.readConversationStream(ctx, id, node, since, send)
			since = max(next, since)
			if errors.Is(err, errConversationStreamUnsupported) {
				send(conversationStreamFrame{event: "conversation_fallback"})
				return
			}
			if err != nil && ctx.Err() == nil && !errors.Is(err, session.ErrSessionUnavailable) {
				_, event := obs.Start(ctx, "gateway", "relay_conversation")
				event.Set("session_id", id)
				event.Fail(err)
				event.End()
			}
			if live {
				// The worker's stream ended, so it is restarting or exiting: reconnect promptly.
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			// Nothing is streaming this chat: read it again only once state or the mesh changes.
			if !g.awaitConversationChange(ctx, &changes, &stop) {
				return
			}
		}
	})
	return frames
}

// conversationRecheck bounds how long an offline chat tab goes without rereading the chat, in case
// a change produced no event (a worker endpoint file written without a state change).
const conversationRecheck = 30 * time.Second

// awaitConversationChange waits for a state or mesh change, or the recheck interval. False when ctx ends.
func (g *Gateway) awaitConversationChange(ctx context.Context, changes *<-chan eventMessage, stop *func()) bool {
	recheck := g.events.recheck
	if recheck == nil {
		timer := time.NewTimer(conversationRecheck)
		defer timer.Stop()
		recheck = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-recheck:
			return true
		case message, ok := <-*changes:
			if !ok { // fell behind: listen again and reread, as a reconnecting browser would
				(*stop)()
				*changes, *stop = g.listen()
				return true
			}
			if message.Topic == "nodes" || message.Topic == "state" &&
				(message.Tables == nil || slices.Contains(message.Tables, "sessions") || slices.Contains(message.Tables, "conversation_events")) {
				return true
			}
		}
	}
}

// readConversationStream sends the chat's entries after since. live reports that an upstream stream was
// open, as opposed to the stored log of a chat no worker serves.
func (g *Gateway) readConversationStream(ctx context.Context, id, node string, since int64, send func(conversationStreamFrame) bool) (next int64, live bool, err error) {
	info, err := session.StoredSession(ctx, g.store.Reader, id)
	if err == nil && (info.Transport != "acp" || !info.ArchivedAt.IsZero()) {
		return since, false, errConversationStreamUnsupported
	}
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return since, false, err
	}
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return since, false, err
	}
	if node == "local" {
		node = local
	}
	if info.NodeID != "" {
		node = info.NodeID
	}
	query := url.Values{"stream": {"1"}, "since": {strconv.FormatInt(since, 10)}}
	path, authorization := "/conversation", ""
	var upstream *url.URL
	var transport http.RoundTripper = network.ConversationWorkerTransport
	if node == "" || node == local {
		record, err := network.ReadWorkerRecord(g.options.Home, id)
		if err != nil {
			// A starting session can already contain prompts accepted directly by the gateway.
			entries, _, truncated, err := network.StoredConversationTail(ctx, g.store.Reader, id, since)
			if err != nil {
				return since, false, session.ErrSessionUnavailable
			}
			if truncated && !send(conversationStreamFrame{event: "conversation_truncated"}) {
				return since, false, ctx.Err()
			}
			for _, entry := range entries {
				if !send(conversationStreamFrame{event: "conversation", seq: entry.Seq, body: network.MustRawJSON(entry)}) {
					return since, false, ctx.Err()
				}
				since = entry.Seq
			}
			return since, false, nil
		}
		upstream = &url.URL{Scheme: "http", Host: record.Address}
		authorization = "Bearer " + record.Secret
	} else {
		upstream, err = g.tailnetTarget(ctx, node)
		if err != nil {
			return since, false, session.ErrSessionUnavailable
		}
		path = "/api/sessions/" + id + "/conversation"
		transport = tailnetRequestTransport(http.MethodGet, path)
	}
	endpoint := upstream.JoinPath(path)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return since, false, err
	}
	request.Header.Set("Accept", "text/event-stream")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	} else if actor, ok := session.ActorFrom(ctx); ok && (actor.Account || actor.Kind == "browser" || actor.Kind == "mcp") {
		request.Header.Set("X-Agentpack-User", actor.User)
	}
	if trace := obs.TraceParent(ctx); trace != "" {
		request.Header.Set("traceparent", trace)
	}
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return since, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusOK && !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return since, false, errConversationStreamUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return since, false, fmt.Errorf("conversation stream: HTTP %d", response.StatusCode)
	}
	for entry, err := range sse.Read(response.Body, &sse.ReadConfig{MaxEventSize: network.MaxConversationSnapshotBytes + 1024}) {
		if err != nil {
			return since, true, err
		}
		if entry.Type == "truncated" || entry.Type == "gap" {
			if !send(conversationStreamFrame{event: "conversation_" + entry.Type}) {
				return since, true, ctx.Err()
			}
			continue
		}
		if entry.Type == "derived" {
			if !send(conversationStreamFrame{event: "conversation_derived", body: json.RawMessage(entry.Data)}) {
				return since, true, ctx.Err()
			}
			continue
		}
		var header struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal([]byte(entry.Data), &header) != nil || header.Seq <= since {
			continue
		}
		if !send(conversationStreamFrame{event: "conversation", seq: header.Seq, body: json.RawMessage(entry.Data)}) {
			return since, true, ctx.Err()
		}
		since = header.Seq
	}
	return since, true, nil
}
