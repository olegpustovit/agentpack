package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

const maxTracePageBytes = 1 << 20

type traceUpload struct {
	Events []obs.Event `json:"events"`
}

func (g *Gateway) ingestTraces(w http.ResponseWriter, r *http.Request) {
	peer, _ := r.Context().Value(replicaPeerKey{}).(string)
	if !g.meshPeer(r.Context(), peer) {
		http.Error(w, "diagnostics require explicit mesh enrollment", http.StatusForbidden)
		return
	}
	roles, err := g.auth.store.Main(r.Context())
	local, nodeErr := g.auth.store.NodeID(r.Context())
	if err != nil || nodeErr != nil {
		writeDiagnostics(w, r, nil, errors.Join(err, nodeErr))
		return
	}
	if roles.MainNode != local || g.logs == nil {
		http.Error(w, "main diagnostics unavailable", http.StatusServiceUnavailable)
		return
	}
	var batch traceUpload
	if !readJSON(w, r, &batch) {
		return
	}
	if len(batch.Events) == 0 || len(batch.Events) > 16 {
		http.Error(w, "invalid diagnostic batch size", http.StatusBadRequest)
		return
	}
	for _, event := range batch.Events {
		trace := obs.ParseTraceParent((obs.Trace{ID: event.TraceID, Span: event.SpanID}).TraceParent())
		if !event.ErrorTrace || event.NodeID == "" || event.TS.IsZero() || trace.ID != event.TraceID || trace.Span != event.SpanID {
			http.Error(w, "invalid diagnostic event", http.StatusBadRequest)
			return
		}
	}
	if err := g.logs.Import(r.Context(), batch.Events); err != nil {
		writeDiagnostics(w, r, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) localTrace(w http.ResponseWriter, r *http.Request) {
	peer, _ := r.Context().Value(replicaPeerKey{}).(string)
	if !g.meshPeer(r.Context(), peer) {
		http.Error(w, "diagnostics require explicit mesh enrollment", http.StatusForbidden)
		return
	}
	events, err := obs.ReadTracePage(r.Context(), g.options.Home, r.PathValue("trace"), r.URL.Query().Get("after"))
	writeDiagnostics(w, r, events, err)
}

func (g *Gateway) forwardErrorTraces(ctx context.Context, targetFor func(context.Context, string) (*url.URL, error)) error {
	roles, err := g.auth.store.Main(ctx)
	if err != nil {
		return err
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil || roles.MainNode == "" || roles.MainNode == local || g.logs == nil {
		return err
	}
	if !g.meshPeer(ctx, roles.MainNode) {
		return errors.New("main gateway is not an approved mesh peer")
	}
	return g.logs.ForwardErrors(ctx, roles.MainNode, func(ctx context.Context, events []obs.Event) error {
		target, err := targetFor(ctx, roles.MainNode)
		if err != nil {
			return err
		}
		body, err := json.Marshal(traceUpload{events})
		if err != nil {
			return err
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, target.JoinPath("/api/logs/ingest").String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("traceparent", obs.TraceParent(ctx))
		client := &http.Client{Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(r)
		if err != nil {
			return errors.New("main diagnostics unreachable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("main diagnostics returned HTTP %d", response.StatusCode)
		}
		return nil
	})
}

func (g *Gateway) runLogForwarding(ctx context.Context) {
	if g.logs == nil {
		return
	}
	node, err := g.auth.store.NodeID(ctx)
	if err != nil {
		_, event := obs.Start(obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, "", "gateway")), "logs.forward", "node identity")
		event.Fail(err)
		event.End()
		return
	}
	ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, node, "gateway"))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		forwardCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		forwardCtx, event := obs.Start(forwardCtx, "logs.forward", "error traces")
		err := g.forwardErrorTraces(forwardCtx, g.tailnetTarget)
		cancel()
		if err != nil && ctx.Err() == nil {
			event.Fail(err)
			event.End()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func fetchPeerTracePage(ctx context.Context, target *url.URL, trace, cursor string) (obs.TracePage, error) {
	endpoint := target.JoinPath("/api/logs/trace/" + trace)
	query := url.Values{"after": {cursor}}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return obs.TracePage{}, err
	}
	request.Header.Set("traceparent", obs.TraceParent(ctx))
	client := &http.Client{Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return obs.TracePage{}, errors.New("trace peer unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return obs.TracePage{}, fmt.Errorf("trace peer returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTracePageBytes+1))
	if err != nil || len(body) > maxTracePageBytes {
		return obs.TracePage{}, errors.New("peer trace page is incomplete or too large")
	}
	var page obs.TracePage
	if json.Unmarshal(body, &page) != nil || len(page.Events) > obs.TracePageSize {
		return page, errors.New("invalid peer trace page")
	}
	var after obs.Event
	if cursor != "" {
		after, err = obs.ParseTraceCursor(trace, cursor)
		if err != nil {
			return page, err
		}
	}
	for i := range page.Events {
		event := page.Events[i].Sanitized()
		identity := obs.ParseTraceParent((obs.Trace{ID: event.TraceID, Span: event.SpanID}).TraceParent())
		if event.TraceID != trace || event.TS.IsZero() || identity.Span != event.SpanID || identity.ID != trace {
			return page, errors.New("peer returned an invalid trace event")
		}
		if cursor != "" && obs.CompareEvents(event, after) <= 0 {
			return page, errors.New("peer trace page did not advance")
		}
		page.Events[i] = event
	}
	if page.NextCursor != "" && len(page.Events) == 0 {
		return page, errors.New("empty peer trace page has a cursor")
	}
	return page, nil
}

func mergeTrace(events, more []obs.Event) []obs.Event {
	seen := make(map[string]bool, len(events)+len(more))
	result := make([]obs.Event, 0, len(events)+len(more))
	for _, group := range [][]obs.Event{events, more} {
		for _, event := range group {
			key := event.NodeID + ":" + event.TraceID + ":" + event.SpanID
			if !seen[key] {
				seen[key] = true
				result = append(result, event)
			}
		}
	}
	slices.SortFunc(result, obs.CompareEvents)
	return result
}

type tracePage struct {
	obs.TracePage
	UnavailableNodes []string `json:"unavailable_nodes,omitempty"`
}

func (g *Gateway) tracePage(ctx context.Context, trace, cursor string, all bool, targetFor func(context.Context, string) (*url.URL, error)) (tracePage, error) {
	trace = strings.ToLower(trace)
	page, err := obs.ReadTracePage(ctx, g.options.Home, trace, cursor)
	if err != nil {
		return tracePage{}, err
	}
	roles, err := g.auth.store.Main(ctx)
	if err != nil {
		return tracePage{}, err
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return tracePage{}, err
	}
	peers := []string{roles.MainNode}
	if all && g.options.Library.Store != nil {
		list, err := g.options.Library.Store.ListPeers()
		if err != nil {
			return tracePage{}, err
		}
		for _, peer := range list {
			if peer.Mesh && peer.Node != roles.MainNode {
				peers = append(peers, peer.Node)
			}
		}
	}
	type result struct {
		page obs.TracePage
		node string
		err  error
	}
	results := make(chan result, len(peers))
	pending := 0
	for _, peer := range peers {
		if peer == "" || peer == local || !g.meshPeer(ctx, peer) {
			continue
		}
		pending++
		obs.Go("gateway.trace_peer", func() {
			got := result{node: peer}
			defer func() { results <- got }()
			peerCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			target, err := targetFor(peerCtx, peer)
			got.err = err
			if err == nil {
				got.page, got.err = fetchPeerTracePage(peerCtx, target, trace, cursor)
			}
		})
	}
	resultPage := tracePage{TracePage: page}
	more := page.NextCursor != ""
	candidates := page.Events
	for range pending {
		got := <-results
		if got.err != nil {
			if event := obs.From(ctx); event != nil {
				event.Fail(got.err)
			}
			resultPage.UnavailableNodes = append(resultPage.UnavailableNodes, got.node)
			continue
		}
		more = more || got.page.NextCursor != ""
		candidates = mergeTrace(candidates, got.page.Events)
	}
	resultPage.Events = candidates
	resultPage.NextCursor = ""
	if len(candidates) > obs.TracePageSize {
		more = true
		resultPage.Events = candidates[:obs.TracePageSize]
	}
	if more && len(resultPage.Events) > 0 {
		resultPage.NextCursor = obs.TraceCursor(resultPage.Events[len(resultPage.Events)-1])
	}
	slices.Sort(resultPage.UnavailableNodes)
	return resultPage, nil
}

func (g *Gateway) traceEvents(ctx context.Context, trace string, all bool, targetFor func(context.Context, string) (*url.URL, error)) ([]obs.Event, error) {
	events := []obs.Event{}
	cursor := ""
	for {
		page, err := g.tracePage(ctx, trace, cursor, all, targetFor)
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if page.NextCursor == "" {
			return events, nil
		}
		cursor = page.NextCursor
	}
}

// TraceEvents keeps offline CLI reads useful and adds the gateway's mesh view when it is running.
func TraceEvents(ctx context.Context, home, trace string, all bool) ([]obs.Event, error) {
	events, err := obs.TraceEvents(ctx, home, trace)
	if err != nil {
		return nil, err
	}
	path := "/api/logs/traces/" + url.PathEscape(trace)
	cursor := ""
	result := events
	for {
		query := url.Values{"page": {"1"}, "after": {cursor}}
		if all {
			query.Set("all_nodes", "1")
		}
		var body bytes.Buffer
		if err := NodeRequestStream(ctx, home, http.MethodGet, path+"?"+query.Encode(), &body, maxTracePageBytes); err != nil {
			return events, nil
		}
		var page tracePage
		if err := json.Unmarshal(body.Bytes(), &page); err != nil {
			return nil, errors.New("invalid gateway trace page")
		}
		result = mergeTrace(result, page.Events)
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("gateway trace page did not advance")
		}
		cursor = page.NextCursor
	}
}
