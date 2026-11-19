package network

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/state"
	"tailscale.com/ipn"
)

const testPublicOrigin = "https://agentpack.example.com"

func publicGateway(t *testing.T, publicURL string) *Gateway {
	t.Helper()
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, auth, testBrowserEmail, "owner-token")
	origin, _ := url.Parse("http://127.0.0.1:7420")
	g := &Gateway{auth: auth, store: auth.store, origin: origin, local: nodeRecord{Secret: strings.Repeat("p", 64)}, options: GatewayOptions{Home: home, PublicURL: publicURL}}
	g.public.serve = func(context.Context) (*ipn.ServeConfig, error) { return nil, errors.New("no tailscale in tests") }
	if err := g.loadPublic(t.Context()); err != nil {
		t.Fatal(err)
	}
	return g
}

func serve(handler http.Handler, method, target, remote string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = remote
	for key, value := range header {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestPublicURLKeepsLocalOriginAndForwardedHeadersNeedLoopback(t *testing.T) {
	g := publicGateway(t, testPublicOrigin)
	handler := g.Handler()
	// AGENTPACK_PUBLIC_URL used to replace the loopback origin, so the local UI got a 403.
	if w := serve(handler, "GET", "http://127.0.0.1:7420/api/sessions", "127.0.0.1:5000", map[string]string{"Cookie": "agentpack_session=owner-token"}); w.Code != 200 {
		t.Fatalf("local UI: %d %s", w.Code, w.Body.String())
	}
	proxied := map[string]string{"X-Forwarded-Host": "agentpack.example.com", "X-Forwarded-Proto": "https", "X-Forwarded-For": "203.0.113.9, 198.51.100.7", "Cookie": "__Host-agentpack_session=owner-token"}
	if w := serve(handler, "GET", "http://127.0.0.1:7420/api/sessions", "127.0.0.1:5000", proxied); w.Code != 200 {
		t.Fatalf("public origin through a loopback proxy: %d %s", w.Code, w.Body.String())
	}
	// The same headers from a non-loopback peer are ignored: the host is unrecognized.
	if w := serve(handler, "GET", "http://gateway.internal/api/sessions", "192.0.2.10:5000", proxied); w.Code != 403 || !strings.Contains(w.Body.String(), "unrecognized gateway host") {
		t.Fatalf("forwarded headers from a remote peer: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:7420/", nil)
	r.RemoteAddr = "192.0.2.10:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	public, _ := g.public.current()
	if source := g.resolveSource(r, public); source.client != "192.0.2.10" {
		t.Fatalf("remote peer chose its limiter key: %+v", source)
	}
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")
	if source := g.resolveSource(r, public); source.client != "198.51.100.7" {
		t.Fatalf("limiter key is not the last forwarded hop: %+v", source)
	}
	if source := g.resolveSource(r, nil); source.client != "127.0.0.1" {
		t.Fatalf("forwarded client trusted while off: %+v", source)
	}
	// Public origins get the __Host- cookie; the loopback origin keeps the plain one.
	login := func(host string, header map[string]string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://"+host+"/api/login", strings.NewReader(`{"email":"owner@example.com","password":"wrong-password"}`))
		r.RemoteAddr = "127.0.0.1:5000"
		for key, value := range header {
			r.Header.Set(key, value)
		}
		source := g.resolveSource(r, public)
		w := httptest.NewRecorder()
		g.auth.issueSession(w, withRequestSource(r, source), User{Email: testBrowserEmail})
		return w.Result().Cookies()[0]
	}
	if cookie := login("127.0.0.1:7420", nil); cookie.Name != "agentpack_session" || cookie.Secure {
		t.Fatalf("local cookie: %+v", cookie)
	}
	if cookie := login("127.0.0.1:7420", map[string]string{"X-Forwarded-Host": "agentpack.example.com", "X-Forwarded-Proto": "https"}); cookie.Name != "__Host-agentpack_session" || !cookie.Secure {
		t.Fatalf("public cookie: %+v", cookie)
	}
}

func TestPublicAccessOffPageRemembersCandidate(t *testing.T) {
	g := publicGateway(t, "")
	handler := g.Handler()
	w := serve(handler, "GET", "http://127.0.0.1:7420/", "127.0.0.1:5000", map[string]string{"X-Forwarded-Host": "agentpack.example.com", "X-Forwarded-Proto": "https"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "Public access is off") {
		t.Fatalf("off page: %d %s", w.Code, w.Body.String())
	}
	if status := g.publicStatus(t.Context()); status.Enabled || status.Candidate != testPublicOrigin {
		t.Fatalf("candidate: %+v", status)
	}
	// Funnel passes the visitor's own Host on; it must not nominate an address.
	serve(handler, "GET", "http://127.0.0.1:7420/", "127.0.0.1:5000", map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https", "Tailscale-Funnel-Request": "?1"})
	if status := g.publicStatus(t.Context()); status.Candidate != testPublicOrigin {
		t.Fatalf("a Funnel visitor chose the candidate: %+v", status)
	}
	config := &ipn.ServeConfig{Foreground: map[string]*ipn.ServeConfig{"session": {
		Web:         map[ipn.HostPort]*ipn.WebServerConfig{"box.tail.ts.net:443": {Handlers: map[string]*ipn.HTTPHandler{"/": {Proxy: "http://127.0.0.1:7420"}}}},
		AllowFunnel: map[ipn.HostPort]bool{"box.tail.ts.net:443": true},
	}}}
	if origin := funnelOrigin(config, "7420"); origin != "https://box.tail.ts.net" {
		t.Fatalf("funnel candidate: %q", origin)
	}
	if origin := funnelOrigin(config, "7421"); origin != "" {
		t.Fatalf("funnel to another port: %q", origin)
	}
}

func setPublicRequest(t *testing.T, g *Gateway, cookie, body string) (int, []map[string]any) {
	t.Helper()
	r := httptest.NewRequest("POST", "http://127.0.0.1:7420/api/settings/public", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("Origin", "http://127.0.0.1:7420")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: cookie})
	} else {
		r.Header.Del("Origin")
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	}
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var lines []map[string]any
	scanner := bufio.NewScanner(w.Body)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) == nil {
			lines = append(lines, line)
		}
	}
	return w.Code, lines
}

func TestPublicAccessNeedsClaimedOwner(t *testing.T) {
	home := t.TempDir()
	auth, err := testUnclaimedAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	unclaimed := &Gateway{auth: auth, store: auth.store, origin: origin, local: nodeRecord{Secret: strings.Repeat("u", 64)}, options: GatewayOptions{Home: home}}
	if code, _ := setPublicRequest(t, unclaimed, "", `{"enabled":true,"origin":"`+testPublicOrigin+`"}`); code != 409 {
		t.Fatalf("unclaimed gateway: %d", code)
	}
	g := publicGateway(t, "")
	testBrowserSession(t, g.auth, "member@example.com", "member-token")
	if code, _ := setPublicRequest(t, g, "member-token", `{"enabled":true,"origin":"`+testPublicOrigin+`"}`); code != 403 {
		t.Fatalf("non-owner: %d", code)
	}
	pinned := publicGateway(t, testPublicOrigin)
	if code, _ := setPublicRequest(t, pinned, "owner-token", `{"enabled":false}`); code != 409 {
		t.Fatalf("pinned switch changed: %d", code)
	}
}

func TestPublicAccessVerifiesItselfBeforeEnabling(t *testing.T) {
	g := publicGateway(t, "")
	gateway := httptest.NewServer(g.Handler())
	defer gateway.Close()
	target, _ := url.Parse(gateway.URL)
	forward := func(keepHost bool) http.Handler {
		return &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			if keepHost {
				r.SetXForwarded()
			} else {
				r.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		}}
	}
	for _, test := range []struct {
		name    string
		handler http.Handler
		failed  string
	}{
		{"another server", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, protectedResourceMetadata(testPublicOrigin))
		}), "reachable"},
		// A server that copies the request header back must not pass for this gateway.
		{"echoes the probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Agentpack-Probe", r.Header.Get("X-Agentpack-Probe"))
			writeJSON(w, protectedResourceMetadata(testPublicOrigin))
		}), "reachable"},
		{"host dropped", forward(false), "mcp"},
		{"reaches itself", forward(true), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := httptest.NewTLSServer(test.handler)
			defer proxy.Close()
			client := proxy.Client()
			client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
			}
			g.public.client = client
			code, lines := setPublicRequest(t, g, "owner-token", `{"enabled":true,"origin":"`+testPublicOrigin+`"}`)
			if code != 200 || len(lines) == 0 {
				t.Fatalf("set public: %d %v", code, lines)
			}
			failed := ""
			for _, line := range lines {
				if check, ok := line["check"].(string); ok && line["ok"] != true && failed == "" {
					failed = check
				}
			}
			final, _ := lines[len(lines)-1]["public"].(map[string]any)
			if failed != test.failed || final["enabled"] != (test.failed == "") {
				t.Fatalf("checks %v", lines)
			}
			var stored string
			_ = g.store.Reader.QueryRow("SELECT value FROM node_settings WHERE node_id=" + state.LocalNodeSQL + " AND key='public'").Scan(&stored)
			if want := map[bool]string{true: testPublicOrigin}[test.failed == ""]; stored != want {
				t.Fatalf("stored public origin %q, want %q", stored, want)
			}
		})
	}
	if w := serve(g.Handler(), "GET", "http://127.0.0.1:7420/api/status", "127.0.0.1:5000", map[string]string{"X-Forwarded-Host": "agentpack.example.com", "X-Forwarded-Proto": "https"}); w.Code != 200 {
		t.Fatalf("public origin after enabling: %d %s", w.Code, w.Body.String())
	}
	if code, lines := setPublicRequest(t, g, "owner-token", `{"enabled":false}`); code != 200 || lines[0]["public"].(map[string]any)["enabled"] != false {
		t.Fatalf("turn off: %d %v", code, lines)
	}
}

func TestUnclaimedGatewayCannotBeClaimedFromTheInternetEvenWhenPinned(t *testing.T) {
	home := t.TempDir()
	auth, err := testUnclaimedAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	g := &Gateway{auth: auth, store: auth.store, origin: origin, local: nodeRecord{Secret: strings.Repeat("p", 64)}, options: GatewayOptions{Home: home, PublicURL: testPublicOrigin}}
	g.public.serve = func(context.Context) (*ipn.ServeConfig, error) { return nil, errors.New("no tailscale in tests") }
	if err := g.loadPublic(t.Context()); err != nil {
		t.Fatal(err)
	}
	proxied := map[string]string{"X-Forwarded-Host": "agentpack.example.com", "X-Forwarded-Proto": "https", "X-Forwarded-For": "203.0.113.9", "Origin": testPublicOrigin, "Content-Type": "application/json"}
	if w := serve(g.Handler(), "POST", "http://127.0.0.1:7420/api/claim", "127.0.0.1:5000", proxied); w.Code == 200 {
		t.Fatalf("claimed from the internet: %s", w.Body.String())
	}
	if w := serve(g.Handler(), "GET", "http://127.0.0.1:7420/api/status", "127.0.0.1:5000", proxied); w.Code != 200 || !strings.Contains(w.Body.String(), `"claimed":false`) {
		t.Fatalf("deploy status check: %d %s", w.Code, w.Body.String())
	}
}
