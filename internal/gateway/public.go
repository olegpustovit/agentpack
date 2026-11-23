package gateway

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/ipn"

	"github.com/olegpustovit/agentpack/internal/gateway/mcp"
	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

// publicAccess is the Public access switch: an HTTPS origin served beside the loopback one.
type publicAccess struct {
	mu        sync.Mutex
	origin    *url.URL // nil while off
	pinned    bool     // AGENTPACK_PUBLIC_URL
	forwarded []string // distinct HTTPS origins a loopback proxy forwarded while off (at most maxForwarded)
	nonce     string   // pending self-probe: sent with the request
	answer    string   // and what only this gateway answers, so a server echoing headers fails
	probe     *url.URL // origin under verification
	client    *http.Client
	serve     func(context.Context) (*ipn.ServeConfig, error)
}

type publicStatus struct {
	Enabled   bool   `json:"enabled"`
	Origin    string `json:"origin,omitempty"`
	Pinned    bool   `json:"pinned,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	// CatchAll: the proxy forwards more than one host name, so anyone could name one; the owner types it.
	CatchAll bool `json:"catch_all,omitempty"`
}

type publicCheck struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

const publicProbePath = "/.well-known/oauth-protected-resource/mcp"

// parsePublicOrigin accepts only a bare HTTPS origin.
func parsePublicOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || len(u.Host) > 253 {
		return nil, errors.New("public URL must be an HTTPS origin")
	}
	if address, err := netip.ParseAddr(u.Hostname()); err == nil && (address.IsLoopback() || address.IsUnspecified()) || strings.EqualFold(u.Hostname(), "localhost") {
		return nil, errors.New("public URL must not be a loopback address")
	}
	return &url.URL{Scheme: "https", Host: strings.ToLower(u.Host)}, nil
}

func (p *publicAccess) current() (*url.URL, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.origin, p.pinned
}

// preferredOrigin names this gateway in links handed to other programs: the public origin while
// it is on, else the loopback one (as AGENTPACK_PUBLIC_URL did before the switch existed).
func (g *Gateway) preferredOrigin() string {
	if public, _ := g.public.current(); public != nil {
		return public.String()
	}
	return g.origin.Scheme + "://" + g.origin.Host
}

// loadPublic restores the switch: AGENTPACK_PUBLIC_URL pins it, otherwise node_settings decides.
func (g *Gateway) loadPublic(ctx context.Context) error {
	if g.options.PublicURL != "" {
		origin, err := parsePublicOrigin(g.options.PublicURL)
		if err != nil {
			return err
		}
		g.public.origin, g.public.pinned = origin, true
		return nil
	}
	var value string
	err := g.store.Reader.QueryRowContext(ctx, "SELECT value FROM node_settings WHERE node_id="+state.LocalNodeSQL+" AND key='public'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || err == nil && value == "" {
		return nil
	}
	if err != nil {
		return err
	}
	origin, err := parsePublicOrigin(value)
	if err != nil {
		return fmt.Errorf("stored public origin: %w", err)
	}
	g.public.origin = origin
	return nil
}

type requestSourceKey struct{}

// requestSource is what the gateway concluded about where a request came from.
type requestSource struct {
	origin  string // origin the request addressed; "" when this gateway does not serve it
	client  string // client address the limiters key on
	proxied bool   // a loopback proxy forwarded it over HTTPS
	host    string
}

func lastHop(header http.Header, name string) string {
	values := header.Values(name)
	if len(values) == 0 {
		return ""
	}
	hops := strings.Split(values[len(values)-1], ",")
	return strings.TrimSpace(hops[len(hops)-1])
}

func peerAddress(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func loopbackPeer(r *http.Request) bool {
	address, err := netip.ParseAddr(peerAddress(r))
	return err == nil && address.IsLoopback()
}

// resolveSource checks the request's host against the loopback origin and, while public access is
// on, the public origin. Forwarded headers count only from a loopback peer: while off they only
// mark a request as proxied, so it is turned away and its host offered as a candidate.
func (g *Gateway) resolveSource(r *http.Request, public *url.URL) requestSource {
	source := requestSource{client: peerAddress(r), host: r.Host}
	loopback := loopbackPeer(r)
	forwardedHost := loopback && r.Header.Get("X-Forwarded-Host") != ""
	source.proxied = loopback && strings.EqualFold(lastHop(r.Header, "X-Forwarded-Proto"), "https")
	if forwardedHost {
		source.host = lastHop(r.Header, "X-Forwarded-Host")
	}
	if public != nil && loopback {
		if hop, err := netip.ParseAddr(lastHop(r.Header, "X-Forwarded-For")); err == nil {
			source.client = hop.String()
		}
	}
	if public != nil && strings.EqualFold(source.host, public.Host) {
		source.origin = public.Scheme + "://" + public.Host
	} else if r.Host == g.origin.Host && (loopback || !g.wideListener()) && !source.proxied && !forwardedHost {
		// A 0.0.0.0 listener is reachable from the LAN, where any client can name this Host.
		source.origin = g.origin.Scheme + "://" + g.origin.Host
	}
	return source
}

// wideListener is a gateway listening on a non-loopback address, which other machines can reach directly.
func (g *Gateway) wideListener() bool {
	address, err := netip.ParseAddrPort(g.origin.Host)
	return err == nil && !address.Addr().IsLoopback()
}

// maxForwarded bounds what a stream of made-up host names can store; two already mean a catch-all proxy.
const maxForwarded = 8

func withRequestSource(r *http.Request, source requestSource) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestSourceKey{}, source))
}

// sourceOf falls back to the connection itself for handlers reached without the gateway middleware.
func sourceOf(r *http.Request) requestSource {
	if source, ok := r.Context().Value(requestSourceKey{}).(requestSource); ok {
		return source
	}
	return requestSource{client: peerAddress(r), host: r.Host}
}

// secureRequest decides Secure and the __Host- cookie name per request: HTTPS origins only.
func secureRequest(r *http.Request) bool {
	return strings.HasPrefix(sourceOf(r).origin, "https://")
}

const publicOffPage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Public access is off</title><main class="auth-shell"><section class="auth-card"><h1>Public access is off</h1><p>This address forwards to an Agentpack gateway that only answers its own machine and tailnet. To use it, open Settings › Sharing on that machine and turn on Public access.</p></section></main></html>`

// servePublicOff answers a proxied HTTPS request while public access is off, remembering its host.
func (g *Gateway) servePublicOff(w http.ResponseWriter, r *http.Request, source requestSource) {
	// Funnel passes the visitor's own Host on, so its candidate comes only from the serve config.
	if origin, err := parsePublicOrigin("https://" + source.host); err == nil && r.Header.Get("Tailscale-Funnel-Request") == "" {
		g.public.mu.Lock()
		// A proxy for one site only ever forwards one host; a second one means anyone can name a host.
		if !slices.Contains(g.public.forwarded, origin.String()) && len(g.public.forwarded) < maxForwarded {
			g.public.forwarded = append(g.public.forwarded, origin.String())
		}
		g.public.mu.Unlock()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, publicOffPage)
}

// serveProbe answers the gateway's own verification request, which must arrive through the
// origin under test before public access is on. It reports the origin this gateway resolved.
func (g *Gateway) serveProbe(w http.ResponseWriter, r *http.Request) bool {
	nonce := r.Header.Get("X-Agentpack-Probe")
	if nonce == "" || r.Method != http.MethodGet || r.URL.Path != publicProbePath {
		return false
	}
	g.public.mu.Lock()
	probe, expected, answer := g.public.probe, g.public.nonce, g.public.answer
	g.public.mu.Unlock()
	if probe == nil || subtle.ConstantTimeCompare([]byte(nonce), []byte(expected)) != 1 {
		return false
	}
	w.Header().Set("X-Agentpack-Probe", answer)
	network.WriteJSON(w, mcp.ProtectedResourceMetadata(g.resolveSource(r, probe).origin))
	return true
}

// funnelOrigin finds a Tailscale Funnel that proxies to port, in the saved or a foreground serve config.
func funnelOrigin(config *ipn.ServeConfig, port string) string {
	if config == nil {
		return ""
	}
	for hostPort, web := range config.Web {
		if !config.AllowFunnel[hostPort] || web == nil {
			continue
		}
		for _, handler := range web.Handlers {
			if handler == nil || handler.Proxy == "" {
				continue
			}
			target := handler.Proxy
			if !strings.Contains(target, "://") {
				target = "http://" + target
			}
			u, err := url.Parse(target)
			if err != nil || u.Port() != port && u.Host != port {
				continue
			}
			host, servePort, err := net.SplitHostPort(string(hostPort))
			if err != nil {
				continue
			}
			if servePort != "443" {
				host = net.JoinHostPort(host, servePort)
			}
			return "https://" + strings.ToLower(host)
		}
	}
	for _, foreground := range config.Foreground {
		if origin := funnelOrigin(foreground, port); origin != "" {
			return origin
		}
	}
	return ""
}

func (g *Gateway) publicStatus(ctx context.Context) publicStatus {
	g.public.mu.Lock()
	origin, pinned, forwarded, serve := g.public.origin, g.public.pinned, slices.Clone(g.public.forwarded), g.public.serve
	g.public.mu.Unlock()
	if origin != nil {
		return publicStatus{Enabled: true, Origin: origin.String(), Pinned: pinned}
	}
	if len(forwarded) > 1 {
		return publicStatus{CatchAll: true}
	}
	candidate := ""
	if len(forwarded) == 1 {
		candidate = forwarded[0]
	} else {
		if serve == nil {
			serve = mesh.Tailscale.GetServeConfig
		}
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		// Tailscale may be absent; then there is simply no Funnel candidate.
		if config, err := serve(ctx); err == nil {
			candidate = funnelOrigin(config, g.origin.Port())
		}
	}
	return publicStatus{Candidate: candidate}
}

func (g *Gateway) writePublic(ctx context.Context, origin *url.URL) error {
	value := ""
	if origin != nil {
		value = origin.String()
	}
	if _, err := g.store.DB.ExecContext(ctx, "INSERT INTO node_settings(node_id,key,value) VALUES ("+state.LocalNodeSQL+",'public',?) ON CONFLICT(node_id,key) DO UPDATE SET value=excluded.value", value); err != nil {
		return err
	}
	g.public.mu.Lock()
	g.public.origin = origin
	if origin == nil {
		g.public.forwarded = nil
	}
	g.public.mu.Unlock()
	return nil
}

// verifyPublic fetches the protected-resource metadata through origin over verified TLS. It
// passes only when the answer carries this gateway's nonce and names origin's MCP resource.
func (g *Gateway) verifyPublic(ctx context.Context, origin *url.URL, report func(publicCheck)) bool {
	nonce, answer := network.RandomToken(), network.RandomToken()
	g.public.mu.Lock()
	if g.public.probe != nil {
		g.public.mu.Unlock()
		report(publicCheck{Check: "reachable", Detail: "another check is already running"})
		return false
	}
	g.public.probe, g.public.nonce, g.public.answer = origin, nonce, answer
	client := g.public.client
	g.public.mu.Unlock()
	defer func() {
		g.public.mu.Lock()
		g.public.probe, g.public.nonce, g.public.answer = nil, "", ""
		g.public.mu.Unlock()
	}()
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	host := origin.Hostname()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+publicProbePath, nil)
	if err != nil {
		report(publicCheck{Check: "reachable", Detail: err.Error()})
		return false
	}
	request.Header.Set("X-Agentpack-Probe", nonce)
	response, err := client.Do(request)
	if err != nil {
		detail := "couldn't reach " + host
		var certificate *tls.CertificateVerificationError
		if errors.As(err, &certificate) {
			detail = host + " has no valid HTTPS certificate"
		}
		report(publicCheck{Check: "reachable", Detail: detail})
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || subtle.ConstantTimeCompare([]byte(response.Header.Get("X-Agentpack-Probe")), []byte(answer)) != 1 {
		report(publicCheck{Check: "reachable", Detail: host + " answers, but not from this gateway"})
		return false
	}
	report(publicCheck{Check: "reachable", OK: true, Detail: "through a proxy at " + host + ", certificate valid"})
	report(publicCheck{Check: "sign_in", OK: true, Detail: "owner account set; new visitors can't claim this gateway"})
	report(publicCheck{Check: "cookies", OK: true, Detail: "HTTPS only, not readable by scripts"})
	var metadata struct {
		Resource string `json:"resource"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&metadata) != nil || metadata.Resource != origin.String()+"/mcp" {
		report(publicCheck{Check: "mcp", Detail: "the proxy doesn't pass the original host name (X-Forwarded-Host or Host)"})
		return false
	}
	report(publicCheck{Check: "mcp", OK: true, Detail: "OAuth metadata and client registration answer at /mcp"})
	return true
}

// setPublic turns public access on (after verification) or off. Progress streams as NDJSON lines:
// one per check, then the resulting status.
func (g *Gateway) setPublic(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool   `json:"enabled"`
		Origin  string `json:"origin,omitempty"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	if _, pinned := g.public.current(); pinned {
		http.Error(w, "public access is set by AGENTPACK_PUBLIC_URL", http.StatusConflict)
		return
	}
	claimed, err := g.auth.claimed(r.Context())
	if err != nil {
		authFailure(w, r, "account state unavailable", err)
		return
	}
	if !claimed {
		http.Error(w, "claim this gateway before turning on public access", http.StatusConflict)
		return
	}
	if !g.requireOwner(w, r) {
		return
	}
	var origin *url.URL
	if input.Enabled {
		if origin, err = parsePublicOrigin(input.Origin); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	controller := http.NewResponseController(w)
	report := func(line any) {
		_ = encoder.Encode(line)
		_ = controller.Flush()
	}
	if origin != nil && !g.verifyPublic(r.Context(), origin, func(check publicCheck) { report(check) }) {
		report(map[string]publicStatus{"public": g.publicStatus(r.Context())})
		return
	}
	if err := g.writePublic(r.Context(), origin); err != nil {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(fmt.Errorf("save public access: %w", err))
		}
		report(map[string]string{"error": "couldn't save the setting"})
		return
	}
	report(map[string]publicStatus{"public": g.publicStatus(r.Context())})
}
