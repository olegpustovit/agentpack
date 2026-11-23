package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

const testTraceParent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"

func observedEvent(t *testing.T, queue *obs.Queue) obs.Event {
	t.Helper()
	select {
	case event := <-queue.Events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("event was not recorded")
		return obs.Event{}
	}
}

func TestHTTPObservationRecordsRouteAndTrustedActorWithoutBodies(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if obs.From(r.Context()) == nil {
			t.Error("request lacks event context")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "private-response")
	})
	queue := obs.NewQueue()
	handler := observeHTTP(mux, mux, queue, obs.BuildMetadata("test", "self", "gateway"))
	request := httptest.NewRequest("GET", "http://localhost/api/sessions/session-one?node=peer", strings.NewReader("private-request"))
	request.Header.Set("traceparent", testTraceParent)
	request.Header.Set("Cookie", "agentpack_session=private-cookie")
	request.Header.Set("Authorization", "Bearer private-authorization")
	request = request.WithContext(session.WithActor(request.Context(), session.Actor{User: "alice@example.com", Kind: "peer"}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	event := observedEvent(t, queue)
	if event.Name != "GET /api/sessions/{id}" || event.Status != 201 || event.TraceID != obs.ParseTraceParent(testTraceParent).ID || event.SessionID != "session-one" || event.User != "alice@example.com" || event.Attrs["node"] != "peer" {
		t.Fatalf("event metadata %+v", event)
	}
	if response.Header().Get("X-Agentpack-Trace") != event.TraceID || event.Attrs["response_bytes"] != int64(len("private-response")) {
		t.Fatalf("trace/bytes missing %+v", event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-request", "private-response", "private-cookie", "private-authorization"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("request observation leaked", secret)
		}
	}
}

func TestHTTPObservationRecoversPanicAndKeepsServing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("handler failed") })
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /bad", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "validation detail", 400) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	queue := obs.NewQueue()
	server := httptest.NewServer(observeHTTP(mux, mux, queue, obs.Metadata{}))
	defer server.Close()
	for _, test := range []struct {
		path string
		code int
	}{{"/panic", 500}, {"/ok", 200}, {"/bad", 400}, {"/healthz", 204}} {
		response, err := server.Client().Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != test.code || response.Header.Get("X-Agentpack-Trace") == "" {
			t.Fatalf("%s status=%d trace=%q", test.path, response.StatusCode, response.Header.Get("X-Agentpack-Trace"))
		}
		if test.path == "/healthz" {
			continue
		}
		event := observedEvent(t, queue)
		if test.path == "/panic" {
			if event.Kind != "panic" || event.Level != "error" || event.Stack == "" || event.Fingerprint == "" {
				t.Fatalf("panic not captured %+v", event)
			}
		} else if event.Level != "info" {
			t.Fatalf("non-5xx was an error: %+v", event)
		}
	}
	if len(queue.Events) != 0 {
		t.Fatal("health check was stored")
	}
}

func TestHTTPObservationAbortsPanicAfterFlushing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /late", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		panic("late failure")
	})
	queue := obs.NewQueue()
	server := httptest.NewServer(observeHTTP(mux, mux, queue, obs.Metadata{}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/late")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("partially written panic response looked complete")
	}
	event := observedEvent(t, queue)
	if event.Kind != "panic" || event.Status != 500 {
		t.Fatalf("late panic metadata %+v", event)
	}
}

func TestHTTPObservationRecordsProxyAbortAsAbortNotPanic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /proxy", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	queue := obs.NewQueue()
	server := httptest.NewServer(observeHTTP(mux, mux, queue, obs.Metadata{}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/proxy")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.ReadAll(response.Body)
	event := observedEvent(t, queue)
	if event.Kind == "panic" || event.Level == "error" || event.Status != 200 {
		t.Fatalf("proxy abort recorded as a failure: %+v", event)
	}
}

func TestHTTPObservationStreamsFinishOnceOnClose(t *testing.T) {
	for _, kind := range []string{"sse", "websocket"} {
		t.Run(kind, func(t *testing.T) {
			queue := obs.NewQueue()
			ready := make(chan struct{})
			mux := http.NewServeMux()
			mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
				if kind == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: ready\n\n")
					w.(http.Flusher).Flush()
					close(ready)
					<-r.Context().Done()
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				close(ready)
				_, _, _ = conn.ReadMessage()
			})
			server := httptest.NewServer(observeHTTP(mux, mux, queue, obs.Metadata{}))
			defer server.Close()
			var closeClient func() error
			if kind == "sse" {
				response, err := server.Client().Get(server.URL + "/stream")
				if err != nil {
					t.Fatal(err)
				}
				closeClient = response.Body.Close
			} else {
				conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1)+"/stream", nil)
				if err != nil {
					t.Fatal(err)
				}
				closeClient = conn.Close
			}
			defer closeClient()
			<-ready
			if len(queue.Events) != 0 {
				t.Fatal("open stream finished its event early")
			}
			if err := closeClient(); err != nil {
				t.Fatal(err)
			}
			event := observedEvent(t, queue)
			code := 200
			if kind == "websocket" {
				code = 101
			}
			if event.Status != code || event.Name != "GET /stream" || len(queue.Events) != 0 {
				t.Fatalf("stream event %+v", event)
			}
		})
	}
}

func TestPeerProxyPropagatesCorrectedTraceAndStripsPrivateHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		if r.URL.Query().Get("node") != "" {
			t.Error("forwarded node routing query")
		}
		w.Header().Set("X-Agentpack-Trace", "spoof")
		w.Header().Set("Set-Cookie", "private-peer-cookie")
		_, _ = io.WriteString(w, "ok")
	}))
	defer peer.Close()
	target, _ := url.Parse(peer.URL)
	gateway := &Gateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) { mesh.ProxyTo(w, r, gateway, "peer", target) })
	queue := obs.NewQueue()
	request := httptest.NewRequest("GET", "http://localhost/api/sessions?node=peer", nil)
	request.Header.Set("traceparent", testTraceParent)
	request.Header.Set("Connection", "traceparent")
	request.Header.Set("Cookie", "private-cookie")
	request.Header.Set("Authorization", "Bearer private-secret")
	response := httptest.NewRecorder()
	observeHTTP(mux, mux, queue, obs.Metadata{}).ServeHTTP(response, request)
	event := observedEvent(t, queue)
	headers := <-seen
	if obs.ParseTraceParent(headers.Get("traceparent")).ID != event.TraceID {
		t.Fatal("peer trace differs from request")
	}
	if headers.Get("Authorization") != "" || headers.Get("Cookie") != "" || response.Header().Get("Set-Cookie") != "" {
		t.Fatal("proxy forwarded private headers")
	}
	if got := response.Header().Values("X-Agentpack-Trace"); len(got) != 1 || got[0] != event.TraceID {
		t.Fatalf("proxy response trace %v", got)
	}
}

func TestWorkerRequestPropagatesTraceWithOrWithoutEvent(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.Header.Get("traceparent"); w.WriteHeader(204) }))
	defer server.Close()
	root := obs.WithTrace(context.Background(), testTraceParent)
	child, event := obs.Start(root, "http", "request")
	defer event.End()
	for _, ctx := range []context.Context{root, child} {
		response, err := network.WorkerRequest(ctx, network.WorkerRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: "private"}, "GET", "/state", "")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if obs.ParseTraceParent(<-seen).ID != obs.ParseTraceParent(testTraceParent).ID {
			t.Fatal("worker lost trace")
		}
	}
}

func BenchmarkMiddleware(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	logs, err := obs.Open(b.TempDir(), obs.StoreOptions{})
	if err != nil {
		b.Fatal(err)
	}
	defer logs.Close()
	for _, instrumented := range []bool{false, true} {
		b.Run(fmt.Sprint(instrumented), func(b *testing.B) {
			var handler http.Handler = mux
			if instrumented {
				handler = observeHTTP(mux, mux, logs, obs.BuildMetadata("v0.13.9", "node-a", "gateway"))
			}
			request := httptest.NewRequest("GET", "http://localhost/api/test", nil)
			for b.Loop() {
				handler.ServeHTTP(httptest.NewRecorder(), request)
			}
		})
	}
}

func TestGatewayPersistsPanicObservation(t *testing.T) {
	home := t.TempDir()
	canonical, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	logs, err := obs.Open(home, obs.StoreOptions{State: canonical, Metadata: obs.BuildMetadata("test", network.LocalStateNode, "gateway")})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	gateway, origin := identityGateway(t, home, nil)
	gateway.logs, gateway.store = logs, canonical
	gateway.options.Assets = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			panic("asset handler failed")
		}
		w.WriteHeader(204)
	})
	handler := gateway.Handler()
	for _, test := range []struct {
		path string
		code int
	}{{"/boom", 500}, {"/ok", 204}, {"/healthz", 200}} {
		request := httptest.NewRequest("GET", origin.String()+test.path, nil)
		request.Header.Set("Cookie", "agentpack_session=must-never-be-stored")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.code {
			t.Fatalf("%s response=%d", test.path, response.Code)
		}
	}
	if err := logs.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "logs", time.Now().UTC().Format(time.DateOnly)+".db")
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(path))+"?agentpack_plain=1&mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("stored requests=%d err=%v", count, err)
	}
	var stack, attrs string
	if err := db.QueryRow("SELECT stacks.text,json(events.attrs) FROM events JOIN stacks ON stacks.hash=events.stack WHERE kind='panic' AND status=500").Scan(&stack, &attrs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stack, "TestGatewayPersistsPanicObservation") || strings.Contains(attrs, "must-never-be-stored") {
		t.Fatal("persisted panic lacks caller stack or exposed cookie")
	}
	if err := db.QueryRow("SELECT count(*) FROM events WHERE kind='panic'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("panic rows=%d err=%v", count, err)
	}
	if err := canonical.Reader.QueryRow("SELECT sum(count) FROM issue_counts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("gateway issue count=%d err=%v", count, err)
	}
}
