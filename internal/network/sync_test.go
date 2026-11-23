package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmaxmax/go-sse"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestStateSyncRequiresEnrollmentAndMatchingSchema(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	if err := library.EnrollPeer("trusted"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	schema := strconv.Itoa(replicationVersion)
	for _, test := range []struct {
		peer, query string
		status      int
	}{
		{"", "schema=" + schema + "&since=0", 403},
		{"unknown", "schema=" + schema + "&since=0", 403},
		{"trusted", "schema=0&since=0", 409},
		{"trusted", "schema=44&since=0", 409}, // a peer still sending its migration count
		{"trusted", "schema=oops&since=0", 400},
		{"trusted", "schema=" + schema + "&since=-1", 400},
		{"trusted", "schema=" + schema + "&since=0&exclude=aa", 400},
		{"trusted", "schema=" + schema + "&since=0", 200},
	} {
		r := httptest.NewRequest("GET", "/api/sync/changes?"+test.query, nil)
		r.Header.Set("X-Agentpack-Peer", "trusted")
		if test.peer != "" {
			r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, test.peer))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%+v: %d %s", test, w.Code, w.Body.String())
		}
	}
	if !peerRoute("GET", "/api/sync/changes") || peerRoute("POST", "/api/sync/changes") {
		t.Fatal("incorrect sync peer route policy")
	}
	if !peerRoute("GET", "/api/sync/events") || peerRoute("POST", "/api/sync/events") {
		t.Fatal("incorrect sync event route policy")
	}
}

func TestStateSyncPagesCommitCompleteVersions(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	if err := library.EnrollPeer("trusted"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	const count = 6
	for i := 0; i < count; i++ {
		if _, err := auth.store.DB.Exec("INSERT INTO conversation_events(session_id,seq,body) VALUES ('large',?,?)", i, strings.Repeat("x", 1<<20)); err != nil {
			t.Fatal(err)
		}
	}
	target, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	since, pages := int64(0), 0
	for {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/sync/changes?schema=%d&since=%d", replicationVersion, since), nil)
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "trusted"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("page %d: %d %s", pages, w.Code, w.Body.String())
		}
		if w.Body.Len() > syncPageLimit {
			t.Fatalf("unbounded page: %d", w.Body.Len())
		}
		var page stateChangePage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if _, err := target.Replicator().ApplyPage(t.Context(), page.Changes, page.SiteID, page.Next); err != nil {
			t.Fatal(err)
		}
		if page.Next == since {
			break
		}
		since = page.Next
		pages++
		if pages > 10 {
			t.Fatal("cursor did not settle")
		}
	}
	if pages < 2 {
		t.Fatal("fixture did not span pages")
	}
	var got int
	if err := target.Reader.QueryRow("SELECT count(*) FROM conversation_events WHERE session_id='large'").Scan(&got); err != nil || got != count {
		t.Fatalf("events: %d %v", got, err)
	}
}

func TestLegacyLibraryPeersCannotReadMeshState(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	if err := library.EnrollOwnMachine("legacy"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	request := func() int {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/sync/changes?schema=%d&since=0", replicationVersion), nil)
		r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "legacy"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(); got != 403 {
		t.Fatalf("legacy peer gained account/history access: %d", got)
	}
	if err := library.EnrollPeer("legacy"); err != nil {
		t.Fatal(err)
	}
	if got := request(); got != 200 {
		t.Fatalf("explicit peer refused: %d", got)
	}
	if err := library.RemovePeer("legacy"); err != nil {
		t.Fatal(err)
	}
	if got := request(); got != 403 {
		t.Fatalf("removed peer admitted: %d", got)
	}
}

func TestMeshEnrollmentMutationsRequireOwner(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.store.DB.Exec("INSERT INTO users(email,role) VALUES ('member@example.com','member')"); err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	if err := library.EnrollPeer("peer"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	// The route table enforces the owner policy; the caller kind only gets the request past the login check.
	mux := routeMux{g, http.NewServeMux()}
	g.registerRegistryReplication(mux)
	for _, method := range []string{"POST", "DELETE"} {
		path := replicationPrefix + "peers"
		if method == "DELETE" {
			path += "/peer"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{"peer":"peer"}`))
		r = r.WithContext(withCaller(session.WithActor(r.Context(), session.BrowserActor("member@example.com")), callerCLI))
		w := httptest.NewRecorder()
		mux.mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("member mutation %s: %d", method, w.Code)
		}
	}
	r := httptest.NewRequest("DELETE", replicationPrefix+"peers/peer", nil)
	r = r.WithContext(withCaller(session.WithActor(r.Context(), session.BrowserActor(testBrowserEmail)), callerCLI))
	w := httptest.NewRecorder()
	mux.mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("owner removal: %d %s", w.Code, w.Body.String())
	}
}

func syncGateway(t *testing.T, id string) (*Gateway, *httptest.Server, *atomic.Int32) {
	t.Helper()
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.store.BindNode(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	library := replicaStore(t)
	if err := library.EnrollPeer("trusted"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, options: GatewayOptions{Library: LibraryOptions{Store: library}}}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	status := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := status.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "trusted")))
	}))
	t.Cleanup(server.Close)
	return g, server, status
}

func TestStatePullConvergesThroughPeerAndHealsPartition(t *testing.T) {
	a, as, _ := syncGateway(t, "a")
	b, bs, partition := syncGateway(t, "b")
	c, _, _ := syncGateway(t, "c")
	if _, err := a.auth.store.DB.Exec("INSERT INTO sessions(id,name,node_id) VALUES ('remote','original','a')"); err != nil {
		t.Fatal(err)
	}
	pull := func(dst *Gateway, src *httptest.Server) error {
		target, _ := url.Parse(src.URL)
		_, _, err := pullState(t.Context(), dst.auth.store, src.Client(), target, nil)
		return err
	}
	if err := pull(b, as); err != nil {
		t.Fatal(err)
	}
	partition.Store(503)
	if err := pull(c, bs); err == nil {
		t.Fatal("partition was ignored")
	}
	partition.Store(0)
	if err := pull(c, bs); err != nil {
		t.Fatal(err)
	}
	if _, err := a.auth.store.DB.Exec("UPDATE sessions SET name='changed' WHERE id='remote'"); err != nil {
		t.Fatal(err)
	}
	if err := pull(b, as); err != nil {
		t.Fatal(err)
	}
	if err := pull(c, bs); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := c.auth.store.Reader.QueryRow("SELECT name FROM sessions WHERE id='remote'").Scan(&name); err != nil || name != "changed" {
		t.Fatalf("indirect replication: %q %v", name, err)
	}
	for _, code := range []int32{404, 409} {
		partition.Store(code)
		if err := pull(c, bs); !errors.Is(err, errSyncNeedsUpdate) {
			t.Fatalf("HTTP %d not Needs update: %v", code, err)
		}
	}
	partition.Store(0)
	if err := pull(c, bs); err != nil {
		t.Fatal(err)
	}
}

func TestStatePeerLoopWakesAndRecoversFromVersionSkew(t *testing.T) {
	source, server, status := syncGateway(t, "source")
	target, _, _ := syncGateway(t, "target")
	endpoint, _ := url.Parse(server.URL)
	wake := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	status.Store(409)
	go func() {
		defer close(done)
		target.runStatePeer(ctx, "trusted", wake, 10*time.Millisecond, func(context.Context, string) (*url.URL, error) { return endpoint, nil })
	}()
	defer func() { cancel(); <-done }()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("sync loop did not reach expected state")
	}
	wait(func() bool {
		v, ok := target.replicaStatus.Load("trusted")
		return ok && v.(replicaContact).NeedsUpdate
	})
	status.Store(0)
	if _, err := source.auth.store.DB.Exec("INSERT INTO sessions(id,node_id,name) VALUES ('loop','source','arrived')"); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	wait(func() bool {
		var count int
		err := target.auth.store.Reader.QueryRow("SELECT count(*) FROM sessions WHERE id='loop'").Scan(&count)
		return err == nil && count == 1
	})
	wait(func() bool {
		v, ok := target.replicaStatus.Load("trusted")
		return ok && v.(replicaContact).Error == ""
	})
	if _, err := source.auth.store.DB.Exec("UPDATE sessions SET name='timer refreshed' WHERE id='loop'"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		var name string
		err := target.auth.store.Reader.QueryRow("SELECT name FROM sessions WHERE id='loop'").Scan(&name)
		return err == nil && name == "timer refreshed"
	})

}

func TestStateEventWakesPeerPullWithoutBrowser(t *testing.T) {
	g := eventsGateway(nil, nil, nil)
	g.options.Library.Store = replicaStore(t)
	if err := g.options.Library.Store.EnrollPeer("peer"); err != nil {
		t.Fatal(err)
	}
	wake := make(chan struct{}, 1)
	g.statePulls.Store("peer", wake)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/events" {
			t.Error("mesh relay used the wrong event endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"topic\":\"state\",\"node\":\"\"}\n\n")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	connected, _ := g.readPeerEvents(t.Context(), server.Client(), "peer", target)
	if !connected {
		t.Fatal("event relay did not connect")
	}
	select {
	case <-wake:
	default:
		t.Fatal("state event did not wake its pull worker")
	}
}

func TestStateEventStreamKeepsPeerAuthenticationAndRevokesRemoval(t *testing.T) {
	g := eventsGateway(nil, nil, nil)
	authTick := make(chan time.Time)
	g.events.checkAuth = authTick
	g.options.Library.Store = replicaStore(t)
	if err := g.options.Library.Store.EnrollPeer("peer"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.registerStateSync(mux)
	for _, peer := range []string{"", "unknown"} {
		r := httptest.NewRequest("GET", "/api/sync/events", nil)
		r.Header.Set("X-Agentpack-Peer", "peer")
		if peer != "" {
			r = r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, peer))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unapproved event subscriber admitted: %d", w.Code)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), replicaPeerKey{}, "peer")))
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/sync/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	events := make(chan string, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for event, err := range sse.Read(response.Body, nil) {
			if err != nil {
				return
			}
			if strings.Contains(event.Data, `"topic":"state"`) {
				events <- event.Data
			}
		}
	}()
	select {
	case <-closed:
		t.Fatal("peer stream was checked as a browser session")
	case authTick <- time.Now():
	case <-time.After(3 * time.Second):
		t.Fatal("peer stream did not receive its authentication check")
	}
	g.emitEvent(eventMessage{Topic: "state"})
	select {
	case data := <-events:
		if !strings.Contains(data, `"topic":"state"`) {
			t.Fatal(data)
		}
	case <-closed:
		t.Fatal("peer stream closed before its hint")
	case <-time.After(3 * time.Second):
		t.Fatal("missing version hint")
	}
	if err := g.options.Library.Store.RemovePeer("peer"); err != nil {
		t.Fatal(err)
	}
	select {
	case authTick <- time.Now():
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("peer stream did not receive its revocation check")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("removed peer kept its stream")
	}
}

func TestStateManagerCancelsItsPeerWorkers(t *testing.T) {
	g, _, _ := syncGateway(t, "manager")
	g.events.topics = []eventTopic{}
	g.events.nodes = func(context.Context) ([]TailnetNode, error) { return nil, nil }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); g.runStateSync(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		if _, ok := g.statePulls.Load("trusted"); ok {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("manager did not start peer worker")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager did not stop peer worker")
	}
	if _, ok := g.statePulls.Load("trusted"); ok {
		t.Fatal("stopped pull worker remained registered")
	}
}

type countingSink struct{ errors atomic.Int32 }

func (s *countingSink) Write(event obs.Event) {
	if event.Level == "error" {
		s.errors.Add(1)
	}
}

func TestStatePeerRecordsARepeatedFailureOnce(t *testing.T) {
	target, _, _ := syncGateway(t, "target")
	sink := &countingSink{}
	ctx, cancel := context.WithCancel(obs.WithSink(t.Context(), sink, obs.Metadata{}))
	var attempts atomic.Int32
	failure := errors.New("peer is down")
	done := make(chan struct{})
	go func() {
		defer close(done)
		target.runStatePeer(ctx, "trusted", nil, time.Millisecond, func(context.Context, string) (*url.URL, error) {
			if attempts.Add(1) == 5 {
				failure = errors.New("peer needs update")
			}
			return nil, failure
		})
	}()
	for attempts.Load() < 10 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if got := sink.errors.Load(); got != 2 {
		t.Fatalf("recorded %d failures for two distinct errors over %d attempts", got, attempts.Load())
	}
}
