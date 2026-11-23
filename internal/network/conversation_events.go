package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
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
	if r.URL.Path != "/api/events" || !validSessionID(id) || len(id) > 128 {
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
		for ctx.Err() == nil {
			next, err := g.readConversationStream(ctx, id, node, since, send)
			since = max(next, since)
			if errors.Is(err, errConversationStreamUnsupported) {
				send(conversationStreamFrame{event: "conversation_fallback"})
				return
			}
			if err != nil && ctx.Err() == nil && !errors.Is(err, errSessionUnavailable) {
				_, event := obs.Start(ctx, "gateway", "relay_conversation")
				event.Set("session_id", id)
				event.Fail(err)
				event.End()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
	return frames
}

func (g *Gateway) readConversationStream(ctx context.Context, id, node string, since int64, send func(conversationStreamFrame) bool) (int64, error) {
	info, err := storedSession(ctx, g.store.Reader, id)
	if err == nil && (info.Transport != "acp" || !info.ArchivedAt.IsZero()) {
		return since, errConversationStreamUnsupported
	}
	if err != nil && !errors.Is(err, errSessionNotFound) {
		return since, err
	}
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return since, err
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
	var transport http.RoundTripper = conversationWorkerTransport
	if node == "" || node == local {
		record, err := workerRecord(g.options.Home, id)
		if err != nil {
			// A starting session can already contain prompts accepted directly by the gateway.
			entries, _, truncated, err := storedConversationTail(ctx, g.store.Reader, id, since)
			if err != nil {
				return since, errSessionUnavailable
			}
			if truncated && !send(conversationStreamFrame{event: "conversation_truncated"}) {
				return since, ctx.Err()
			}
			for _, entry := range entries {
				if !send(conversationStreamFrame{event: "conversation", seq: entry.Seq, body: mustRawJSON(entry)}) {
					return since, ctx.Err()
				}
				since = entry.Seq
			}
			return since, nil
		}
		upstream = &url.URL{Scheme: "http", Host: record.Address}
		authorization = "Bearer " + record.Secret
	} else {
		upstream, err = g.tailnetTarget(ctx, node)
		if err != nil {
			return since, errSessionUnavailable
		}
		path = "/api/sessions/" + id + "/conversation"
		transport = tailnetRequestTransport(http.MethodGet, path)
	}
	endpoint := upstream.JoinPath(path)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return since, err
	}
	request.Header.Set("Accept", "text/event-stream")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	} else if actor, ok := actorFrom(ctx); ok && (actor.Account || actor.Kind == "browser" || actor.Kind == "mcp") {
		request.Header.Set("X-Agentpack-User", actor.User)
	}
	if trace := obs.TraceParent(ctx); trace != "" {
		request.Header.Set("traceparent", trace)
	}
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return since, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusOK && !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		return since, errConversationStreamUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return since, fmt.Errorf("conversation stream: HTTP %d", response.StatusCode)
	}
	for entry, err := range sse.Read(response.Body, &sse.ReadConfig{MaxEventSize: maxConversationSnapshotBytes + 1024}) {
		if err != nil {
			return since, err
		}
		if entry.Type == "truncated" || entry.Type == "gap" {
			if !send(conversationStreamFrame{event: "conversation_" + entry.Type}) {
				return since, ctx.Err()
			}
			continue
		}
		if entry.Type == "derived" {
			if !send(conversationStreamFrame{event: "conversation_derived", body: json.RawMessage(entry.Data)}) {
				return since, ctx.Err()
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
			return since, ctx.Err()
		}
		since = header.Seq
	}
	return since, nil
}
