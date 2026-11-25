package gateway

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

// policy is who may call a route on this gateway. The zero value requires a login.
type policy uint8

const (
	account policy = iota // a browser login, this machine's CLI, an MCP client or an enrolled peer
	public                // anyone who passed the origin checks
	owner                 // an enabled owner account (requireOwner)
	cli                   // this machine's CLI only
)

// peerAccess is whether a mesh peer may call a route, for itself or for a browser using ?node=.
type peerAccess uint8

const (
	peerNone  peerAccess = iota
	peerRead             // the verified peer identity is enough
	peerWrite            // the peer must also forward an enabled account
)

// timeoutClass picks the CLI/worker client timeout and the tailnet transport for a route.
type timeoutClass uint8

const (
	timeoutDefault timeoutClass = iota // 2 s client, fast tailnet transport for reads
	timeoutSlow                        // 2 s client, extended tailnet transport
	timeoutProbe                       // 2 s client, tailnet probe transport
	timeoutTrace                       // 5 s client: a trace read may ask every peer
	timeoutMinute                      // 1 min: MCP refresh may ask Main, a login discovers its server
	timeoutLong                        // 2 min: starts, stops, prompts and content transfers
)

func (class timeoutClass) client() time.Duration {
	switch class {
	case timeoutLong:
		return 2 * time.Minute
	case timeoutMinute:
		return time.Minute
	case timeoutTrace:
		return 5 * time.Second
	}
	return 2 * time.Second
}

// route is one row of the gateway's route table. The mux, the peer allow list, client timeouts,
// tailnet transports, ETags and session routing are all derived from routeTable.
type route struct {
	pattern string
	policy  policy
	peer    peerAccess
	timeout timeoutClass
	session bool   // resolve the session row once and route the request to its owning node
	noETag  bool   // streams, status and configuration are never answered with 304
	worker  string // forwarded unchanged to the session's worker at /conversation+worker
}

var routeTable = []route{
	{pattern: "/", policy: public},
	{pattern: "GET /healthz", policy: public},
	{pattern: "GET /mcp-oauth/callback", policy: public},
	{pattern: "/api/"}, // JSON 404 for every unknown API path
	{pattern: "GET /api/config", policy: public, noETag: true},
	{pattern: "GET /api/status", policy: public, noETag: true},
	{pattern: "POST /api/claim", policy: public},
	{pattern: "POST /api/login", policy: public},
	{pattern: "POST /api/logout"},
	{pattern: "POST /api/logout-all"},
	{pattern: "GET /api/events", peer: peerRead, noETag: true},
	{pattern: "GET /api/identity", peer: peerRead},
	{pattern: "GET /api/machine", peer: peerRead},
	{pattern: "GET /api/version/latest"},
	{pattern: "GET /api/nodes"},
	{pattern: "GET /api/topology"},
	{pattern: "GET /api/node/update", peer: peerRead},
	{pattern: "POST /api/node/update", policy: owner, peer: peerWrite},
	{pattern: "POST /api/node/stop", policy: owner, timeout: timeoutLong},
	{pattern: "POST /api/node/stop-agents"},
	{pattern: "POST /api/telemetry"},
	{pattern: "GET /api/debug/timings"},
	{pattern: "POST /api/debug/timings"},
	{pattern: "GET /api/debug/pprof/{name...}", policy: cli},
	{pattern: "GET /api/cache/{cache_key}", peer: peerRead, timeout: timeoutLong},

	{pattern: "GET /api/settings"},
	{pattern: "POST /api/settings/public"}, // setPublic checks the claim before the owner
	{pattern: "POST /api/settings/sharing", policy: owner},
	{pattern: "GET /api/push/key"},
	{pattern: "POST /api/push/subscription"},
	{pattern: "DELETE /api/push/subscription"},
	{pattern: "POST /api/push/test"},
	{pattern: "GET /api/sync/changes", peer: peerRead},
	{pattern: "GET /api/sync/events", peer: peerRead, noETag: true},
	{pattern: "POST /api/sync/main/release", peer: peerRead}, // machine protocol: the new Main asks the old one to step down
	{pattern: "GET /api/limits", peer: peerRead, timeout: timeoutSlow},
	{pattern: "POST /api/limits/refresh", peer: peerWrite},
	{pattern: "GET /api/usage"},
	{pattern: "GET /api/agents", peer: peerRead},
	{pattern: "POST /api/agents/{harness}/login", policy: owner, peer: peerWrite},
	{pattern: "GET /api/agents/{harness}/login/{id}", policy: owner, peer: peerRead},
	{pattern: "POST /api/agents/{harness}/login/{id}", policy: owner, peer: peerWrite},
	{pattern: "DELETE /api/agents/{harness}/login/{id}", policy: owner, peer: peerWrite},
	{pattern: "POST /api/agents/{harness}/logout", policy: owner, peer: peerWrite},
	{pattern: "GET /api/agents/defaults", peer: peerRead},
	{pattern: "PUT /api/agents/defaults", policy: owner},
	{pattern: "GET /api/omp/settings", peer: peerRead},
	{pattern: "GET /api/omp/models", peer: peerRead},
	{pattern: "PUT /api/omp/settings/{key}", policy: owner},
	{pattern: "DELETE /api/omp/settings/{key}", policy: owner},

	{pattern: "POST /api/logs/ingest", peer: peerRead},
	{pattern: "GET /api/logs/trace/{trace}", peer: peerRead},
	{pattern: "GET /api/logs/issues"},
	{pattern: "GET /api/logs/issues/{fingerprint}"},
	{pattern: "GET /api/logs/traces/{trace}", timeout: timeoutTrace},
	{pattern: "POST /api/logs/issues/{fingerprint}/resolve"},
	{pattern: "POST /api/logs/issues/{fingerprint}/reopen"},
	{pattern: "POST /api/logs/issues/{fingerprint}/ignore"},

	{pattern: "POST /api/mcp/servers", policy: owner, peer: peerWrite, timeout: timeoutMinute},
	{pattern: "GET /api/mcp/credentials", timeout: timeoutMinute},
	{pattern: "POST /api/mcp/credentials", timeout: timeoutMinute},
	{pattern: "POST /api/mcp/credentials/revoke-all", timeout: timeoutMinute},
	{pattern: "DELETE /api/mcp/credentials/{id}", timeout: timeoutMinute},
	{pattern: "POST /api/mcp/credentials/{id}/approvals", timeout: timeoutMinute},
	{pattern: "DELETE /api/mcp/credentials/{id}/approvals", timeout: timeoutMinute},
	{pattern: "GET /api/mcp/credentials/{id}/secret", timeout: timeoutMinute},
	{pattern: "POST /api/mcp/credentials/{id}/refresh", peer: peerRead, timeout: timeoutMinute},
	{pattern: "PUT /api/projects/mcp-login", peer: peerWrite, timeout: timeoutMinute},
	{pattern: "POST /api/mcp/logins", timeout: timeoutMinute},
	{pattern: "GET /api/mcp/logins/{login}", timeout: timeoutMinute},

	{pattern: "/api/library/", timeout: timeoutSlow}, // 503 while no Library store is configured
	{pattern: "GET /api/library/catalog", timeout: timeoutSlow},
	{pattern: "GET /api/library/packages", peer: peerRead, timeout: timeoutSlow},
	{pattern: "GET /api/library/agentpacks", peer: peerRead, timeout: timeoutSlow},
	{pattern: "POST /api/library/agentpacks", peer: peerWrite, timeout: timeoutSlow},
	{pattern: "GET /api/library/agentpacks/{id}", peer: peerRead, timeout: timeoutSlow},
	{pattern: "PATCH /api/library/agentpacks/{id}", peer: peerWrite, timeout: timeoutSlow},
	{pattern: "DELETE /api/library/agentpacks/{id}", peer: peerWrite, timeout: timeoutSlow},
	{pattern: "GET /api/library/agentpacks/{id}/conflict", peer: peerRead, timeout: timeoutSlow},
	{pattern: "POST /api/library/agentpacks/{id}/resolve", peer: peerWrite, timeout: timeoutSlow},
	{pattern: "GET /api/library/agentpacks/{id}/revisions/{revision}/restore-preview", peer: peerRead, timeout: timeoutSlow},
	{pattern: "POST /api/library/agentpacks/{id}/revisions/{revision}/restore", peer: peerWrite, timeout: timeoutSlow},
	{pattern: "POST /api/library/latest", peer: peerRead, timeout: timeoutSlow},
	{pattern: "POST /api/library/draft-state", peer: peerRead, timeout: timeoutSlow},
	{pattern: "GET /api/library/drafts", timeout: timeoutSlow},
	{pattern: "POST /api/library/drafts", timeout: timeoutSlow},
	{pattern: "DELETE /api/library/drafts/{id}", timeout: timeoutSlow},
	{pattern: "POST /api/library/drafts/{id}/review", timeout: timeoutSlow},
	{pattern: "GET " + replicationPrefix + "peers", timeout: timeoutSlow},
	{pattern: "POST " + replicationPrefix + "peers", policy: owner, timeout: timeoutSlow},
	{pattern: "DELETE " + replicationPrefix + "peers/{peer}", policy: owner, timeout: timeoutSlow},
	{pattern: "POST " + replicationPrefix + "sync", policy: owner, timeout: timeoutSlow},

	{pattern: "GET /api/recent", peer: peerRead},
	{pattern: "DELETE /api/recent", peer: peerWrite},
	{pattern: "GET /api/packages", peer: peerRead},
	{pattern: "POST /api/packages", peer: peerWrite},
	{pattern: "GET /api/projects", peer: peerRead},
	{pattern: "GET /api/projects/files", peer: peerRead},
	{pattern: "PUT /api/projects/settings", policy: owner, peer: peerWrite, timeout: timeoutLong},
	{pattern: "GET /api/folders", peer: peerRead},
	{pattern: "POST /api/folders", peer: peerWrite},
	{pattern: "GET /api/folders/found", peer: peerRead, timeout: timeoutProbe},
	{pattern: "GET /api/worktrees", peer: peerRead},
	{pattern: "POST /api/worktrees/remove", policy: owner, peer: peerWrite},
	{pattern: "POST /api/worktrees/push", policy: owner, peer: peerWrite},
	{pattern: "GET /api/agent-snapshot"},
	{pattern: "GET /api/agent-sessions", peer: peerRead, timeout: timeoutProbe},
	{pattern: "POST /api/local/sessions/{id}/presence", policy: cli},
	{pattern: "DELETE /api/local/sessions/{id}/presence", policy: cli},

	{pattern: "GET /api/sessions", peer: peerRead},
	{pattern: "POST /api/sessions", peer: peerWrite, timeout: timeoutLong},
	{pattern: "GET /api/sessions/{id}", peer: peerRead}, // the replicated row; read on any node
	{pattern: "PATCH /api/sessions/{id}", peer: peerWrite, session: true},
	{pattern: "GET /api/sessions/{id}/shells", peer: peerRead, session: true},
	{pattern: "POST /api/sessions/{id}/detach-shell", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/fork", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/harness", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/tools", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/stop", peer: peerWrite, session: true, timeout: timeoutLong},
	{pattern: "POST /api/sessions/{id}/restart", peer: peerWrite, session: true, timeout: timeoutLong},
	{pattern: "POST /api/sessions/{id}/resume", peer: peerWrite, session: true},
	{pattern: "GET /api/sessions/{id}/terminal", peer: peerWrite, session: true, noETag: true},
	{pattern: "GET /api/sessions/{id}/output", peer: peerRead, session: true, noETag: true},
	{pattern: "POST /api/sessions/{id}/input", peer: peerWrite, session: true},
	{pattern: "GET /api/sessions/{id}/blobs/{sha}", peer: peerRead, session: true, timeout: timeoutLong},
	{pattern: "GET /api/sessions/{id}/blobs/{sha}/held", peer: peerRead, session: true}, // this node's verified copy, never fetched
	{pattern: "GET /api/sessions/{id}/git", peer: peerRead, session: true},
	{pattern: "POST /api/sessions/{id}/git/commit", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/git/pr", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/git/merge", peer: peerWrite, session: true},
	{pattern: "GET /api/sessions/{id}/conversation", peer: peerRead, session: true},
	{pattern: "POST /api/sessions/{id}/conversation/prompt", peer: peerWrite, session: true, timeout: timeoutLong},
	{pattern: "POST /api/sessions/{id}/conversation/prompt/images", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/conversation/message", peer: peerWrite, session: true, timeout: timeoutLong},
	// show is only called by a chat's own agent, through the gateway of the machine the chat runs on.
	{pattern: "POST /api/sessions/{id}/conversation/show", session: true},
	{pattern: "POST /api/sessions/{id}/conversation/config", peer: peerWrite, session: true},
	{pattern: "POST /api/sessions/{id}/conversation/blobs", peer: peerWrite, session: true, timeout: timeoutLong},
	{pattern: "POST /api/sessions/{id}/conversation/permission", peer: peerWrite, session: true, worker: "/permission"},
	{pattern: "POST /api/sessions/{id}/conversation/revert", peer: peerWrite, session: true, worker: "/revert"},
	{pattern: "POST /api/sessions/{id}/conversation/cancel", peer: peerWrite, session: true, worker: "/cancel"},
	{pattern: "POST /api/sessions/{id}/conversation/elicitation", peer: peerWrite, session: true, worker: "/elicitation"},
	{pattern: "POST /api/sessions/{id}/conversation/goal", peer: peerWrite, session: true, worker: "/goal"},
	{pattern: "PATCH /api/sessions/{id}/conversation/goal", peer: peerWrite, session: true, worker: "/goal"},
	{pattern: "DELETE /api/sessions/{id}/conversation/goal", peer: peerWrite, session: true},
	{pattern: "PATCH /api/sessions/{id}/conversation/queue/{queueId}", peer: peerWrite, session: true, worker: "/queue/{queueId}"},
	{pattern: "DELETE /api/sessions/{id}/conversation/queue/{queueId}", peer: peerWrite, session: true, worker: "/queue/{queueId}"},
	{pattern: "POST /api/sessions/{id}/conversation/queue/{queueId}/steer", peer: peerWrite, session: true, worker: "/queue/{queueId}/steer"},
}

// etag reports whether the row's GET and HEAD responses get a content ETag.
func (row route) etag() bool {
	path := row.pattern
	if _, rest, found := strings.Cut(row.pattern, " "); found {
		path = rest
	}
	return !row.noETag && strings.HasPrefix(path, "/api/")
}

var routeIndex = sync.OnceValue(func() map[string]route {
	index := make(map[string]route, len(routeTable))
	for _, row := range routeTable {
		if _, duplicate := index[row.pattern]; duplicate {
			panic("duplicate gateway route " + row.pattern)
		}
		index[row.pattern] = row
	}
	return index
})

// routeShapes matches a method and path to its row without a gateway, for clients and the peer filter.
var routeShapes = sync.OnceValue(func() *http.ServeMux {
	mux := http.NewServeMux()
	for _, row := range routeTable {
		mux.HandleFunc(row.pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
})

func routeFor(method, path string) (route, bool) {
	_, pattern := routeShapes().Handler(&http.Request{Method: method, URL: &url.URL{Path: path}})
	row, ok := routeIndex()[pattern]
	return row, ok
}

// peerRoute reports whether a peer may call method and path: a clean path
// matching a peer row of routeTable, whose content-addressed segments are valid.
func peerRoute(method, path string) bool {
	if method == http.MethodHead || !strings.HasPrefix(path, "/api/") {
		return false
	}
	parts := strings.Split(path[1:], "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, `\`) {
			return false
		}
	}
	row, ok := routeFor(method, path)
	if !ok || row.peer == peerNone {
		return false
	}
	last := parts[len(parts)-1]
	switch row.pattern {
	case "GET /api/cache/{cache_key}":
		return cache.ValidKey(last)
	case "GET /api/logs/trace/{trace}":
		decoded, err := hex.DecodeString(last)
		return err == nil && len(decoded) == 16 && strings.Trim(last, "0") != ""
	case "GET /api/sessions/{id}/blobs/{sha}":
		_, err := network.ValidateBlobSHA(last)
		return err == nil
	case "GET /api/sessions/{id}/blobs/{sha}/held":
		_, err := network.ValidateBlobSHA(parts[len(parts)-2])
		return err == nil
	}
	return true
}

// apiRequestTimeout bounds a CLI call to the gateway's API.
func apiRequestTimeout(method, path string) time.Duration {
	path, _, _ = strings.Cut(path, "?")
	row, _ := routeFor(method, path)
	return row.timeout.client()
}

func slowTailnetRequest(method, path string) bool {
	row, _ := routeFor(method, path)
	return method != http.MethodGet && method != http.MethodHead || row.timeout == timeoutSlow || row.timeout == timeoutProbe
}

// RequestTransport is the tailnet transport whose timeouts suit method and path.
func (g *Gateway) RequestTransport(method, path string) http.RoundTripper {
	if row, _ := routeFor(method, path); row.timeout == timeoutProbe {
		return mesh.ProbeTransport
	}
	if slowTailnetRequest(method, path) {
		return mesh.MutationTransport
	}
	return mesh.Transport
}

// callerKind is the entry point a request came through. Each entry point sets it once;
// handlers read it instead of re-deriving trust from the Authorization header.
type callerKind uint8

const (
	callerUnknown callerKind = iota
	callerCLI                // this machine's CLI, holding the private local secret
	callerPeer               // an enrolled mesh peer, verified by ServePeer
	callerMCP                // an agentpack MCP tool call dispatched in-process
	callerBrowser            // everything else: cookies and public origins
)

type callerKey struct{}

func withCaller(ctx context.Context, kind callerKind) context.Context {
	return context.WithValue(ctx, callerKey{}, kind)
}

func callerOf(ctx context.Context) callerKind {
	kind, _ := ctx.Value(callerKey{}).(callerKind)
	return kind
}

type sessionRowKey struct{}

// sessionRow returns the row the route table resolved for this request's session, if any.
func sessionRow(ctx context.Context, id string) (session.SessionInfo, bool) {
	info, ok := ctx.Value(sessionRowKey{}).(session.SessionInfo)
	return info, ok && info.ID == id
}

// routeMux binds handlers to routeTable rows. A pattern missing from the table panics on
// the first gateway build, which TestRouteTableCoversMux turns into a test failure.
type routeMux struct {
	g   *Gateway
	mux *http.ServeMux
}

// routeRegistrar is what register functions bind to: routeMux on the gateway, or a bare
// *http.ServeMux in tests that exercise handlers without the gateway's access checks.
type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func (m routeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(handler))
}

func (m routeMux) Handle(pattern string, handler http.Handler) {
	row, ok := routeIndex()[pattern]
	if !ok {
		panic("gateway route " + pattern + " is missing from routeTable")
	}
	m.mux.Handle(pattern, m.g.serveRoute(row, handler))
}
