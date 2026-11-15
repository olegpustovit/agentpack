package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func TestLargePeerTraceUsesBoundedPagesWithoutTruncation(t *testing.T) {
	source, reader := traceGateway(t, "source"), traceGateway(t, "reader")
	if err := source.options.Library.Store.EnrollPeer("reader"); err != nil {
		t.Fatal(err)
	}
	if err := reader.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.auth.store.DB.Exec("INSERT INTO mesh_settings(key,value) VALUES ('main_node','source')"); err != nil {
		t.Fatal(err)
	}
	trace := obs.NewTrace().ID
	events := make([]obs.Event, 1600)
	for i := range events {
		events[i] = obs.Event{TS: time.Now().Add(-time.Minute).Truncate(time.Microsecond).Add(time.Duration(i) * time.Microsecond), TraceID: trace, SpanID: fmt.Sprintf("%016x", i+1), NodeID: "source", Kind: "http", Level: "info", Attrs: map[string]any{}}
		for j := range 4 {
			events[i].Attrs[fmt.Sprintf("detail_%d", j)] = strings.Repeat("x", 4096)
		}
	}
	if err := source.logs.Import(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	source.registerDiagnostics(mux)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("after") == "" && calls.Load() > 1 {
			t.Error("peer page cursor was lost")
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "reader")))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	got, err := reader.traceEvents(t.Context(), trace, false, func(context.Context, string) (*url.URL, error) { return target, nil })
	if err != nil || len(got) != len(events) {
		t.Fatalf("large trace incomplete: %d %v", len(got), err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 16<<20 {
		t.Fatalf("fixture did not exceed old transport cap: %d", len(body))
	}
	if calls.Load() < 100 {
		t.Fatalf("trace did not paginate: %d", calls.Load())
	}
	for i, event := range got {
		if event.SpanID != events[i].SpanID {
			t.Fatalf("out-of-order or duplicate span at %d", i)
		}
	}
	server.Close()
	page, err := reader.tracePage(t.Context(), trace, "", false, func(context.Context, string) (*url.URL, error) { return target, nil })
	if err != nil || len(page.UnavailableNodes) != 1 || page.UnavailableNodes[0] != "source" {
		t.Fatalf("missing peer was not disclosed: %+v %v", page, err)
	}
}

func traceGateway(t *testing.T, node string) *Gateway {
	t.Helper()
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	if err := g.auth.store.BindNode(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	g.options.Library.Store = replicaStore(t)
	logs, err := obs.Open(home, obs.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	g.logs = logs
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Error(err)
		}
	})
	return g
}

func TestErrorTraceForwardingSurvivesOfflineSourceAndRetries(t *testing.T) {
	source, main := traceGateway(t, "source"), traceGateway(t, "main")
	for _, g := range []*Gateway{source, main} {
		if err := g.options.Library.Store.EnrollPeer("source"); err != nil {
			t.Fatal(err)
		}
		if err := g.options.Library.Store.EnrollPeer("main"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.auth.store.DB.Exec("INSERT INTO mesh_settings(key,value) VALUES ('main_node','main')"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, parent := obs.Start(obs.WithSink(t.Context(), source.logs, obs.Metadata{NodeID: "source", Proc: "gateway"}), "http", "failure parent")
	_, failure := obs.Start(ctx, "worker", "failure child")
	failure.Fail(errors.New("test failure"))
	failure.End()
	parent.End()
	_, healthy := obs.Start(obs.WithSink(t.Context(), source.logs, obs.Metadata{NodeID: "source", Proc: "gateway"}), "http", "healthy request")
	healthy.End()
	mux := http.NewServeMux()
	main.registerDiagnostics(mux)
	var down atomic.Bool
	down.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "source"))
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	targetFor := func(context.Context, string) (*url.URL, error) { return target, nil }
	if err := source.forwardErrorTraces(t.Context(), targetFor); err == nil {
		t.Fatal("offline main acknowledged trace upload")
	}
	down.Store(false)
	for range 2 {
		if err := source.forwardErrorTraces(t.Context(), targetFor); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.logs.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id   string
		want int
	}{{failure.TraceID, 2}, {healthy.TraceID, 0}} {
		events, err := obs.TraceEvents(t.Context(), main.options.Home, test.id)
		if err != nil || len(events) != test.want {
			t.Fatalf("main trace after source stopped: %+v %v", events, err)
		}
		for _, event := range events {
			if event.NodeID != "source" {
				t.Fatalf("rewrote original node: %+v", event)
			}
		}
	}
	other := traceGateway(t, "other")
	if err := other.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	if err := source.options.Library.Store.EnrollPeer("other"); err != nil {
		t.Fatal(err)
	}
	extra := obs.Event{TS: time.Now(), TraceID: failure.TraceID, SpanID: obs.NewTrace().Span, NodeID: "other", Kind: "http", Name: "unsent sibling", Level: "info", Attrs: map[string]any{}}
	other.logs.Write(extra)
	if err := other.logs.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	otherMux := http.NewServeMux()
	other.registerDiagnostics(otherMux)
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "source"))
		otherMux.ServeHTTP(w, r)
	}))
	defer otherServer.Close()
	otherTarget, _ := url.Parse(otherServer.URL)
	readTarget := func(_ context.Context, node string) (*url.URL, error) {
		if node == "other" {
			return otherTarget, nil
		}
		return target, nil
	}
	for _, all := range []bool{false, true} {
		events, err := source.traceEvents(t.Context(), failure.TraceID, all, readTarget)
		want := 2
		if all {
			want = 3
		}
		if err != nil || len(events) != want {
			t.Fatalf("merged trace all=%v: %+v %v", all, events, err)
		}
	}
	if _, err := source.auth.store.DB.Exec("DELETE FROM mesh_settings WHERE key='main_node'"); err != nil {
		t.Fatal(err)
	}
	if err := source.forwardErrorTraces(t.Context(), func(context.Context, string) (*url.URL, error) {
		t.Fatal("forwarded with no main gateway")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPeerDiagnosticRoutesRequireExplicitMeshEnrollment(t *testing.T) {
	g := traceGateway(t, "main")
	if _, err := g.auth.store.DB.Exec("INSERT INTO mesh_settings(key,value) VALUES ('main_node','main')"); err != nil {
		t.Fatal(err)
	}
	if err := g.options.Library.Store.EnrollOwnMachine("legacy"); err != nil {
		t.Fatal(err)
	}
	if err := g.options.Library.Store.EnrollPeer("trusted"); err != nil {
		t.Fatal(err)
	}
	trace := obs.NewTrace()
	event := obs.Event{TS: time.Now(), TraceID: trace.ID, SpanID: trace.Span, NodeID: "source", Kind: "worker", Name: "failure", Level: "error", ErrorTrace: true, Attrs: map[string]any{}}
	body, _ := json.Marshal(traceUpload{[]obs.Event{event}})
	mux := http.NewServeMux()
	g.registerDiagnostics(mux)
	for _, peer := range []string{"", "unknown", "legacy", "trusted"} {
		for _, test := range []struct{ method, path string }{{"POST", "/api/logs/ingest"}, {"GET", "/api/logs/trace/" + trace.ID}} {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(string(body)))
			r.Header.Set("X-Agentpack-Peer", "trusted")
			if peer != "" {
				r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, peer))
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			want := http.StatusForbidden
			if peer == "trusted" {
				want = http.StatusOK
				if test.method == "POST" {
					want = http.StatusNoContent
				}
			}
			if w.Code != want {
				t.Fatalf("%s %s peer=%q: %d %s", test.method, test.path, peer, w.Code, w.Body.String())
			}
			if !peerRoute(test.method, test.path) {
				t.Fatalf("peer route missing: %+v", test)
			}
		}
	}
	for _, path := range []string{"/api/logs/trace/bad", "/api/logs/trace/" + strings.Repeat("0", 32), "/api/logs/trace/" + trace.ID + "/extra", "/api/logs/issues"} {
		if peerRoute("GET", path) {
			t.Fatalf("allowed peer diagnostic route %s", path)
		}
	}
}

func TestPeerDiagnosticIngestValidatesWholeBatch(t *testing.T) {
	g := traceGateway(t, "main")
	if _, err := g.auth.store.DB.Exec("INSERT INTO mesh_settings(key,value) VALUES ('main_node','main')"); err != nil {
		t.Fatal(err)
	}
	if err := g.options.Library.Store.EnrollPeer("source"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.registerDiagnostics(mux)
	for _, body := range []string{`{"events":[]}`, `{"events":[{"error_trace":true,"node_id":"source","trace_id":"bad"}]}`, `{"events":[{"error_trace":false}]}`, `{"events":[],"unknown":true}`, strings.Repeat("x", (1<<20)+1)} {
		r := httptest.NewRequest("POST", "/api/logs/ingest", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "source"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid upload: %d %s", w.Code, w.Body.String())
		}
	}
	events, err := obs.Query(t.Context(), g.options.Home, obs.Filter{})
	if err != nil || len(events) != 0 {
		t.Fatalf("invalid batch saved events: %+v %v", events, err)
	}
	if _, err := g.auth.store.DB.Exec("UPDATE mesh_settings SET value='other' WHERE key='main_node'"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/logs/ingest", strings.NewReader(`{"events":[]}`))
	r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "source"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-main accepted upload: %d", w.Code)
	}
}
