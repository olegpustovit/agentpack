package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
)

func TestPeerRoutesAreBounded(t *testing.T) {
	for _, path := range []string{"/api/setup", "/api/login", "/api/node/stop", "/oauth/token", "/mcp", "/api/sessions/../terminal", "/api/sessions/x/stop/extra"} {
		if peerRoute("POST", path) || peerRoute("GET", path) {
			t.Fatalf("unsafe route %s", path)
		}
	}
	if !peerRoute("GET", "/api/sessions/x/terminal") || !peerRoute("POST", "/api/sessions/x/restart") || !peerRoute("POST", "/api/node/update") || !peerRoute("GET", "/api/machine") {
		t.Fatal("terminal routes unavailable")
	}
	routes := []struct{ method, path string }{
		{"GET", "/api/agent-sessions"},
		{"GET", "/api/sessions/x/conversation"},
		{"POST", "/api/sessions/x/conversation/prompt"},
		{"POST", "/api/sessions/x/conversation/prompt/images"},
		{"POST", "/api/sessions/x/conversation/cancel"},
		{"POST", "/api/sessions/x/conversation/permission"},
		{"POST", "/api/sessions/x/conversation/elicitation"},
		{"POST", "/api/sessions/x/conversation/config"},
		{"POST", "/api/sessions/x/conversation/blobs"},
		{"POST", "/api/sessions/x/fork"},
		{"PATCH", "/api/sessions/x/conversation/queue/q1"},
		{"DELETE", "/api/sessions/x/conversation/queue/q1"},
		{"POST", "/api/sessions/x/conversation/queue/q1/steer"},
		{"POST", "/api/sessions/x/conversation/goal"},
		{"PATCH", "/api/sessions/x/conversation/goal"},
		{"DELETE", "/api/sessions/x/conversation/goal"},
		{"GET", "/api/folders"},
		{"GET", "/api/folders/found"},
		{"GET", "/api/sessions/x/blobs/" + strings.Repeat("a", 64)},
	}
	for _, route := range routes {
		if !peerRoute(route.method, route.path) {
			t.Errorf("route %s %s not available to peer", route.method, route.path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/sessions/../conversation/prompt/images"},
		{"POST", "/api/sessions/x/conversation/config-options"},
		{"GET", "/api/sessions/x/conversation/permission"},
		{"POST", "/api/sessions/x/blobs/" + strings.Repeat("a", 64)},
		{"GET", "/api/sessions/x/blobs/not-a-sha"},
		{"GET", "/api/sessions/x\\..\\conversation"},
		{"POST", "/api/sessions/x/conversation/queue/.."},
		{"GET", "/api/sessions/x/conversation/goal"},
		{"POST", "/api/sessions/x/conversation/queue/q1/drop"},
	} {
		if peerRoute(route.method, route.path) {
			t.Errorf("unsafe peer route accepted: %s %s", route.method, route.path)
		}
	}
}

func TestSlowTailnetRequestsUseExtendedTransport(t *testing.T) {
	for _, request := range []struct {
		method string
		path   string
		want   bool
	}{
		{"GET", "/api/agent-sessions", true},
		{"GET", "/api/limits", true},
		{"GET", "/api/library/index", true},
		{"POST", "/api/sessions", true},
		{"GET", "/api/projects", false},
	} {
		if got := slowTailnetRequest(request.method, request.path); got != request.want {
			t.Errorf("slowTailnetRequest(%q, %q) = %t, want %t", request.method, request.path, got, request.want)
		}
	}
}

func TestAgentSessionRouteUsesBoundedProbeTransport(t *testing.T) {
	if got := (&Gateway{}).RequestTransport("GET", "/api/agent-sessions"); got != mesh.ProbeTransport {
		t.Fatal("agent session probe should use its 60-second response timeout")
	}
	if got := (&Gateway{}).RequestTransport("GET", "/api/projects"); got != mesh.Transport {
		t.Fatal("ordinary read should retain the five-second response timeout")
	}
}

func TestLimitsPeerRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/limits", true},
		{"POST", "/api/limits/refresh", true},
		{"POST", "/api/limits", false},
		{"GET", "/api/limits/refresh", false},
		{"DELETE", "/api/limits", false},
		{"GET", "/api/limits/extra", false},
	} {
		if got := peerRoute(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s allowed=%v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestLibraryPeerRoutesCoverConflictsAndVersions(t *testing.T) {
	for _, test := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/library/agentpacks/abc/conflict", true},
		{"POST", "/api/library/agentpacks/abc/resolve", true},
		{"POST", "/api/library/latest", true},
		{"GET", "/api/library/latest", false},
		{"POST", "/api/library/agentpacks/abc/conflict", false},
		{"GET", "/api/library/agentpacks/abc/resolve", false},
	} {
		if got := peerRoute(test.method, test.path); got != test.want {
			t.Errorf("%s %s: got %v", test.method, test.path, got)
		}
	}
}

// A session route is either reachable through a peer or
// named here as local-only, so a new route cannot silently fail remotely.
func TestPeerPatternsCoverSessionRoutes(t *testing.T) {
	// show is only called by a chat's own agent, through the gateway of the machine the chat runs on.
	localOnly := map[string]bool{"GET /api/sessions/{id}": true, "POST /api/sessions/{id}/conversation/show": true}
	for _, row := range routeTable {
		if strings.Contains(row.pattern, " /api/sessions/{id}") && !localOnly[row.pattern] && row.peer == peerNone {
			t.Errorf("%q has no peer access; give it one or name it in localOnly", row.pattern)
		}
	}
	for _, pattern := range peerPatterns() {
		method, path, _ := strings.Cut(pattern, " ")
		path = strings.NewReplacer("{cache_key}", strings.Repeat("a", 64), "{trace}", "0123456789abcdef0123456789abcdef", "{sha}", strings.Repeat("b", 64)).Replace(path)
		path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x")
		if !peerRoute(method, path) {
			t.Errorf("peerRoute rejects its own pattern %s (%s %s)", pattern, method, path)
		}
	}
	for _, path := range []string{"/api/sessions/x/resume", "/api/sessions/x/conversation/revert"} {
		if !peerRoute("POST", path) || peerRoute("GET", path) || peerRoute("HEAD", path) {
			t.Errorf("POST %s must be the only peer method", path)
		}
	}
	for _, path := range []string{"/api/sessions//resume", "/api/sessions/../resume", `/api/sessions/a\b/stop`, "/api/sessions/x/stop/", "/api//sessions"} {
		if peerRoute("POST", path) || peerRoute("GET", path) {
			t.Errorf("unclean path %q passes", path)
		}
	}
}

func TestSyncHTTPResponseKeepsPeerReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{}
	for _, reachable := range []bool{true, false} {
		if !reachable {
			server.Close()
		}
		page, err := fetchStatePage(t.Context(), server.Client(), target, -1, nil)
		if err == nil || page.reachable != reachable || errors.Is(err, errSyncNeedsUpdate) != reachable {
			t.Fatalf("reachability = %v, error = %v, want reachable = %v", page.reachable, err, reachable)
		}
		g.replicaStatus.Store("peer", replicaContact{At: time.Now(), Reachable: page.reachable, NeedsUpdate: errors.Is(err, errSyncNeedsUpdate), Error: err.Error()})
		if online, last := g.nodeContact("peer", "local"); online != reachable || !last.IsZero() {
			t.Fatalf("online = %v, last sync = %v", online, last)
		}
	}
}

func TestBindTailnetNodeOnlyWhileUsingSiteIdentity(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{auth: auth, store: auth.store}
	if err := g.BindNode(t.Context(), "test-node"); err != nil {
		t.Fatal(err)
	}
	g.store = nil // no store is involved once the stable ID is cached
	if err := g.BindNode(t.Context(), "test-node"); err != nil {
		t.Fatal(err)
	}
	if err := g.BindNode(t.Context(), "different-node"); err == nil {
		t.Fatal("accepted a different stable identity")
	}
}
