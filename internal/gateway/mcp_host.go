package gateway

import (
	"net/http"

	"github.com/olegpustovit/agentpack/internal/gateway/mcp"
)

// The gateway is the mcp.Host its MCP tools and OAuth server run on.
var _ mcp.Host = (*Gateway)(nil)

// ServeAPI dispatches an MCP tool's REST call in-process as callerMCP, so it passes the same
// policy as any other caller, including remote-node routing and session ownership checks.
func (g *Gateway) ServeAPI(w http.ResponseWriter, r *http.Request, node string) {
	r = r.WithContext(withCaller(r.Context(), callerMCP))
	r.URL.Scheme, r.URL.Host, r.Host = "http", g.local.Address, g.local.Address
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	handler := g.Handler()
	if node != "" && g.mcpNodes != nil {
		handler = g.mcpNodes(node)
	} else if node != "" {
		query := r.URL.Query()
		query.Set("node", node)
		r.URL.RawQuery = query.Encode()
	}
	handler.ServeHTTP(w, r)
}

func (g *Gateway) Emit(topic string)                                  { g.emitEvent(eventMessage{Topic: topic}) }
func (g *Gateway) Origin(r *http.Request) string                      { return sourceOf(r).origin }
func (g *Gateway) ClientKey(r *http.Request) (key string, local bool) { return limiterKey(r) }

// Changes wakes its reader on every gateway event; it closes once stop is called.
func (g *Gateway) Changes() (<-chan struct{}, func()) {
	events, stop := g.listen()
	changes := make(chan struct{}, 1)
	go func() {
		defer close(changes)
		for range events {
			select {
			case changes <- struct{}{}:
			default:
			}
		}
	}()
	return changes, stop
}

// BrowserLogin is the session cookie of a signed-in browser.
func (g *Gateway) BrowserLogin(r *http.Request) (string, bool) {
	if !g.auth.authenticated(r) {
		return "", false
	}
	cookie, err := r.Cookie(cookieName(r))
	if err != nil {
		return "", false
	}
	return cookie.Value, true
}
