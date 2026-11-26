package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

// Host is what the tailnet layer needs from the gateway it serves.
type Host interface {
	// PeerRoute reports whether a peer may call method and path.
	PeerRoute(method, path string) bool
	// RequestTransport is the tailnet transport whose timeouts suit method and path.
	RequestTransport(method, path string) http.RoundTripper
	// ServePeer answers a request from an allowed tailnet identity.
	ServePeer(w http.ResponseWriter, r *http.Request, self *Peer, identity Identity)
	// BindNode publishes this machine's stable tailnet identity.
	BindNode(ctx context.Context, id string) error
	// MeshPeer reports whether node id is an enrolled mesh peer.
	MeshPeer(id string) bool
	// ServeConversation serves a conversation from replicated history.
	ServeConversation(w http.ResponseWriter, r *http.Request)
	// Authenticated returns a check that the viewer behind r is still signed in.
	Authenticated(r *http.Request) func() bool
	// Done is closed when the gateway stops.
	Done() <-chan struct{}
}

// Proxy forwards a peer route to node id. A mesh peer that is unreachable still serves its
// conversation from replicated history.
func (c *Cache) Proxy(w http.ResponseWriter, r *http.Request, store *state.Store, host Host, id string) {
	if !host.PeerRoute(r.Method, r.URL.Path) {
		http.Error(w, "route unavailable on remote nodes", 400)
		return
	}
	target, err := c.Target(r.Context(), store, id)
	if err != nil {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && host.MeshPeer(id) {
			host.ServeConversation(w, r)
			return
		}
		status := http.StatusBadGateway
		if host.MeshPeer(id) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return
	}
	ProxyTo(w, r, host, id, target)
}

// ProxyTo forwards r to the gateway at target, carrying the caller's account but never its credentials.
func ProxyTo(w http.ResponseWriter, r *http.Request, host Host, id string, target *url.URL) {
	address := target.Host
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(out *httputil.ProxyRequest) {
		out.SetURL(target)
		request := out.Out
		request.Host = address
		query := request.URL.Query()
		query.Del("node")
		request.URL.RawQuery = query.Encode()
		request.Header.Del("Authorization")
		if trace := obs.TraceParent(r.Context()); trace != "" {
			request.Header.Set("traceparent", trace)
		}
		request.Header.Del("Cookie")
		request.Header.Del("X-Agentpack-User")
		request.Header.Del("X-Agentpack-Browser")
		request.Header.Del("X-Agentpack-Agent")
		if actor, ok := session.ActorFrom(r.Context()); ok && (actor.Account || actor.Kind == "browser" || actor.Kind == "mcp") {
			request.Header.Set("X-Agentpack-User", actor.User)
			if actor.Agent != "" {
				request.Header.Set("X-Agentpack-Agent", actor.Agent)
			}
			// An agent never carries the person's right to start in a new folder.
			if actor.Agent == "" && actor.CanStartNewFolder() {
				request.Header.Set("X-Agentpack-Browser", "1")
			}
		}
		request.Header.Del("Origin")
		request.Header.Del("Forwarded")
		request.Header.Del("X-Forwarded-For")
		request.Header.Del("X-Forwarded-Host")
		request.Header.Del("X-Forwarded-Proto")
	}
	proxy.Transport = host.RequestTransport(r.Method, r.URL.Path)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && host.MeshPeer(id) {
			host.ServeConversation(w, r)
			return
		}
		log.Printf("tailnet proxy failed for node %s: %v", id, err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "remote node connection failed; the gateway logged the network error", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode >= 500 && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/conversation") && host.MeshPeer(id) {
			return errors.New("remote conversation is temporarily unavailable")
		}

		response.Header.Del("Set-Cookie")
		response.Header.Del("X-Agentpack-Trace")
		if response.Header.Get("Cache-Control") == "" {
			response.Header.Set("Cache-Control", "no-store")
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			// The proxy clears response.Body once it owns the upgraded connection, so keep this reference.
			go closeUpgradeOnEnd(r.Context(), host.Done(), response.Body, time.Second, host.Authenticated(r))
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

// closeUpgradeOnEnd holds an upgraded proxy connection open until its request ends, and closes it
// when the gateway stops or the viewer's login no longer holds.
func closeUpgradeOnEnd(ctx context.Context, done <-chan struct{}, connection io.Closer, every time.Duration, authenticated func() bool) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			_ = connection.Close()
			return
		case <-ticker.C:
			if !authenticated() {
				_ = connection.Close()
				return
			}
		}
	}
}

// PeerHandler serves the tailnet listener at address: it admits only allowed tailnet identities
// calling peer routes, answers who this machine is, and hands everything else to the host.
func (c *Cache) PeerHandler(store *state.Store, host Host, self *Peer, address string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address || r.Header.Get("Origin") != "" || r.URL.Query().Get("node") != "" {
			http.Error(w, "invalid peer request", 403)
			return
		}
		if r.URL.Path != "/peer" && !host.PeerRoute(r.Method, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		remote, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "peer identity required", 403)
			return
		}
		ip, err := netip.ParseAddr(remote)
		if err != nil {
			http.Error(w, "peer identity required", 403)
			return
		}
		config, err := ReadSharingConfig(store)
		if err != nil || !config.Enabled {
			http.Error(w, "sharing disabled", 403)
			return
		}
		identity, err := c.Whois(r.Context(), ip)
		if err != nil {
			http.Error(w, "peer identity unavailable", 403)
			return
		}
		if !allowedIdentity(self, identity, ip, config) {
			http.Error(w, "peer is not allowed", 403)
			return
		}
		host.ServePeer(w, r, self, identity)
	})
}

type peerConnections struct {
	sync.Mutex
	connections map[net.Conn]bool
}
type peerListener struct {
	net.Listener
	tracking *peerConnections
}
type peerConnection struct {
	net.Conn
	tracking *peerConnections
}

func (l peerListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &peerConnection{connection, l.tracking}
	l.tracking.Lock()
	l.tracking.connections[wrapped] = true
	l.tracking.Unlock()
	return wrapped, nil
}
func (c *peerConnection) Close() error {
	c.tracking.Lock()
	delete(c.tracking.connections, c)
	c.tracking.Unlock()
	return c.Conn.Close()
}
func (t *peerConnections) close() {
	t.Lock()
	connections := make([]net.Conn, 0, len(t.connections))
	for connection := range t.connections {
		connections = append(connections, connection)
	}
	t.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

// Serve keeps a peer listener on this machine's tailnet address while sharing is on,
// rebinding when the address or the sharing policy changes.
func (c *Cache) Serve(ctx context.Context, store *state.Store, host Host) {
	var server *http.Server
	var tracking *peerConnections
	var bound string
	var policy string
	closeServer := func() {
		if server != nil {
			_ = server.Close()
			tracking.close()
			server = nil
			bound = ""
		}
	}
	defer closeServer()
	// The loop retries every five seconds, so log a problem when it changes, not on every tick.
	var problem string
	report := func(err error) {
		text := ""
		if err != nil {
			text = err.Error()
		}
		if text != problem && text != "" {
			log.Printf("tailnet sharing is not listening: %s", text)
		}
		problem = text
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		config, err := ReadSharingConfig(store)
		if err != nil || !config.Enabled {
			closeServer()
			report(err)
		} else {
			status, err := Snapshot(ctx)
			if err == nil {
				err = host.BindNode(ctx, status.Self.ID)
			}
			address := Address(status.Self)
			if err != nil || address == "" {
				closeServer()
				if err == nil {
					err = errors.New("this machine has no Tailscale address")
				}
				report(err)
			} else {
				currentPolicy, _ := json.Marshal(struct {
					Config SharingConfig
					Self   *Peer
				}{config, status.Self})
				signature := string(currentPolicy)
				if server == nil || bound != address || policy != signature {
					closeServer()
					listener, err := net.Listen("tcp", address)
					report(err)
					if err == nil {
						tracking = &peerConnections{connections: make(map[net.Conn]bool)}
						server = &http.Server{Handler: c.PeerHandler(store, host, status.Self, address), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
						bound = address
						policy = signature
						serving, peer := server, peerListener{listener, tracking}
						obs.Go("gateway.peer_listener", func() { _ = serving.Serve(peer) })
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
