package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
)

func peerPatterns() []string {
	var patterns []string
	for _, row := range routeTable {
		if row.peer != peerNone {
			patterns = append(patterns, row.pattern)
		}
	}
	return patterns
}

// Every routeTable row is bound by a fully configured gateway, and every bound pattern has a row
// (routeMux panics otherwise), so policy metadata cannot drift from the mux.
func TestRouteTableCoversMux(t *testing.T) {
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	g.options.Library.Store = store
	g.options.Assets = http.NotFoundHandler()
	mux := g.routes().mux
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	for _, row := range routeTable {
		method, path, found := strings.Cut(row.pattern, " ")
		if !found {
			method, path = http.MethodGet, row.pattern
		}
		if row.pattern == "/api/library/" {
			continue // bound only while no Library store is configured
		}
		path = strings.ReplaceAll(wildcard.ReplaceAllString(path, "x"), "{name...}", "x")
		_, pattern := mux.Handler(&http.Request{Method: method, URL: &url.URL{Path: path}})
		if pattern != row.pattern {
			t.Errorf("%s: %s %s is served by %q", row.pattern, method, path, pattern)
		}
	}
	g.options.Library.Store = nil
	if _, pattern := g.routes().mux.Handler(httptest.NewRequest(http.MethodGet, "/api/library/packages", nil)); pattern != "/api/library/" {
		t.Errorf("library without a store is served by %q", pattern)
	}
}

// The policy of every route, pinned from the handlers and lists it replaced.
func TestRoutePoliciesArePinned(t *testing.T) {
	publicRoutes := []string{"/", "GET /healthz", "GET /mcp-oauth/callback", "GET /api/config", "GET /api/status", "POST /api/claim", "POST /api/login"}
	ownerRoutes := []string{
		"POST /api/node/update", "POST /api/node/stop", "POST /api/packages", "POST /api/mcp/servers", "POST /api/settings/sharing",
		"POST " + replicationPrefix + "peers", "DELETE " + replicationPrefix + "peers/{peer}", "POST " + replicationPrefix + "sync",
		"POST /api/agents/{harness}/login", "GET /api/agents/{harness}/login/{id}", "POST /api/agents/{harness}/login/{id}",
		"DELETE /api/agents/{harness}/login/{id}", "POST /api/agents/{harness}/logout",
		"PUT /api/omp/settings/{key}", "DELETE /api/omp/settings/{key}", "POST /api/worktrees/remove", "POST /api/worktrees/push",
	}
	cliRoutes := []string{"GET /api/debug/pprof/{name...}", "POST /api/local/sessions/{id}/presence", "DELETE /api/local/sessions/{id}/presence"}
	// A peer may call these with its verified identity alone; every other peer route needs a forwarded account.
	identityOnly := []string{"POST /api/library/latest", "POST /api/library/draft-state", "POST /api/logs/ingest", "POST /api/mcp/credentials/{id}/refresh", "POST /api/sync/main/release"}
	noETag := []string{"GET /api/events", "GET /api/sync/events", "GET /api/status", "GET /api/config", "GET /api/sessions/{id}/terminal", "GET /api/sessions/{id}/output"}
	for _, row := range routeTable {
		want := account
		switch {
		case slices.Contains(publicRoutes, row.pattern):
			want = public
		case slices.Contains(ownerRoutes, row.pattern):
			want = owner
		case slices.Contains(cliRoutes, row.pattern):
			want = cli
		}
		if row.policy != want {
			t.Errorf("%s: policy %d, want %d", row.pattern, row.policy, want)
		}
		if row.peer != peerNone {
			wantPeer := peerWrite
			if strings.HasPrefix(row.pattern, "GET ") && row.pattern != "GET /api/sessions/{id}/terminal" || slices.Contains(identityOnly, row.pattern) {
				wantPeer = peerRead
			}
			if row.peer != wantPeer {
				t.Errorf("%s: peer access %d, want %d", row.pattern, row.peer, wantPeer)
			}
		}
		if row.noETag != slices.Contains(noETag, row.pattern) && strings.Contains(row.pattern, "/api/") {
			t.Errorf("%s: noETag %t", row.pattern, row.noETag)
		}
		// routeMeshRequest used to resolve every /api/sessions/<id>/... route and every non-GET /api/sessions/<id>.
		_, path, _ := strings.Cut(row.pattern, " ")
		wantSession := strings.HasPrefix(path, "/api/sessions/{id}") && row.pattern != "GET /api/sessions/{id}"
		if row.session != wantSession {
			t.Errorf("%s: session %t, want %t", row.pattern, row.session, wantSession)
		}
	}
	slices.Sort(identityOnly)
	var peers []string
	for _, pattern := range peerPatterns() {
		if row := routeIndex()[pattern]; row.peer == peerRead && !strings.HasPrefix(pattern, "GET ") {
			peers = append(peers, pattern)
		}
	}
	slices.Sort(peers)
	if !slices.Equal(peers, identityOnly) {
		t.Errorf("identity-only peer writes = %v", peers)
	}
	// The peer allow list before the route table, verbatim.
	old := []string{
		"GET /api/cache/{cache_key}", "POST /api/logs/ingest", "GET /api/logs/trace/{trace}", "GET /api/sync/changes", "GET /api/sync/events", "POST /api/sync/main/release",
		"GET /api/library/packages", "GET /api/library/agentpacks", "POST /api/library/agentpacks", "POST /api/library/latest", "POST /api/library/draft-state",
		"GET /api/library/agentpacks/{id}", "PATCH /api/library/agentpacks/{id}", "DELETE /api/library/agentpacks/{id}", "GET /api/library/agentpacks/{id}/conflict",
		"POST /api/library/agentpacks/{id}/resolve", "GET /api/library/agentpacks/{id}/revisions/{revision}/restore-preview",
		"POST /api/library/agentpacks/{id}/revisions/{revision}/restore",
		"GET /api/identity", "GET /api/machine", "GET /api/events", "GET /api/agent-sessions", "GET /api/recent", "DELETE /api/recent", "GET /api/packages",
		"POST /api/packages", "POST /api/mcp/servers", "GET /api/projects", "GET /api/folders", "GET /api/folders/found", "GET /api/worktrees",
		"POST /api/worktrees/remove", "POST /api/worktrees/push", "GET /api/limits", "POST /api/limits/refresh", "GET /api/agents",
		"POST /api/agents/{harness}/login", "GET /api/agents/{harness}/login/{id}", "POST /api/agents/{harness}/login/{id}",
		"DELETE /api/agents/{harness}/login/{id}", "POST /api/agents/{harness}/logout", "GET /api/omp/settings", "GET /api/omp/models",
		"GET /api/node/update", "POST /api/node/update", "POST /api/mcp/credentials/{id}/refresh",
		"GET /api/sessions", "POST /api/sessions", "PATCH /api/sessions/{id}", "GET /api/sessions/{id}", "GET /api/sessions/{id}/shells",
		"POST /api/sessions/{id}/detach-shell", "GET /api/sessions/{id}/terminal", "GET /api/sessions/{id}/output", "GET /api/sessions/{id}/conversation",
		"GET /api/sessions/{id}/blobs/{sha}", "POST /api/sessions/{id}/restart", "POST /api/sessions/{id}/resume", "POST /api/sessions/{id}/stop",
		"POST /api/sessions/{id}/input", "POST /api/sessions/{id}/fork", "POST /api/sessions/{id}/harness", "POST /api/sessions/{id}/tools",
		"GET /api/sessions/{id}/git", "POST /api/sessions/{id}/git/commit", "POST /api/sessions/{id}/git/pr", "POST /api/sessions/{id}/git/merge",
		"POST /api/sessions/{id}/conversation/prompt", "POST /api/sessions/{id}/conversation/prompt/images", "POST /api/sessions/{id}/conversation/message",
		"POST /api/sessions/{id}/conversation/permission", "POST /api/sessions/{id}/conversation/cancel", "POST /api/sessions/{id}/conversation/elicitation",
		"POST /api/sessions/{id}/conversation/config", "POST /api/sessions/{id}/conversation/blobs", "POST /api/sessions/{id}/conversation/revert",
		"POST /api/sessions/{id}/conversation/goal", "PATCH /api/sessions/{id}/conversation/goal", "DELETE /api/sessions/{id}/conversation/goal",
		"PATCH /api/sessions/{id}/conversation/queue/{queueId}", "DELETE /api/sessions/{id}/conversation/queue/{queueId}",
		"POST /api/sessions/{id}/conversation/queue/{queueId}/steer",
	}
	got := peerPatterns()
	slices.Sort(old)
	slices.Sort(got)
	if !slices.Equal(old, got) {
		t.Errorf("peer routes changed:\n got %v\nwant %v", got, old)
	}
}

// A session write resolves its row once (GW-F13): the route table keeps it in the request context
// for the ownership check and the worker lookup.
func TestSessionWriteReadsItsRowOnce(t *testing.T) {
	home := t.TempDir()
	g, origin := identityGateway(t, home, nil)
	const id, secret = "session-once", "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	owner := session.BrowserActor(testBrowserEmail)
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Transport: "acp", StartedBy: &owner}); err != nil {
		t.Fatal(err)
	}
	var forwarded atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/conversation/cancel" && r.Header.Get("Authorization") == "Bearer "+secret {
			forwarded.Add(1)
		}
		network.WriteJSON(w, map[string]bool{"ok": true})
	}))
	t.Cleanup(worker.Close)
	data, err := json.Marshal(network.WorkerRecord{ID: id, Address: strings.TrimPrefix(worker.URL, "http://"), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, g.auth, testBrowserEmail, "once")
	handler := g.Handler()
	before := session.SessionRowReads.Load()
	r := httptest.NewRequest(http.MethodPost, origin.String()+"/api/sessions/"+id+"/conversation/cancel", strings.NewReader("{}"))
	r.Header.Set("Origin", origin.String())
	r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "once"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || forwarded.Load() != 1 {
		t.Fatalf("cancel: %d %s (forwarded %d)", w.Code, w.Body.String(), forwarded.Load())
	}
	if reads := session.SessionRowReads.Load() - before; reads != 1 {
		t.Fatalf("session row read %d times, want 1", reads)
	}
}
