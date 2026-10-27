package network

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGatewayAuthenticationHostAndOrigin(t *testing.T) {
	auth, err := newGatewayAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	gateway := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: t.TempDir()}, stop: func() {}}
	handler := gateway.Handler()
	check := func(method, path, host, origin, token string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, "https://"+host+path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s host=%s origin=%s: %d %s", method, path, host, origin, w.Code, w.Body.String())
		}
	}
	check("GET", "/healthz", "agentpack.example", "", "", 200)
	check("GET", "/api/sessions", "agentpack.example", "", "", 401)
	check("GET", "/api/sessions", "evil.example", "", "", 403)
	check("POST", "/api/node/stop", "agentpack.example", "https://evil.example", "", 403)
	check("POST", "/api/node/stop", "agentpack.example", "", "", 403)
	check("GET", "/api/sessions", "127.0.0.1:7420", "", gateway.local.Secret, 200)
	check("GET", "/api/sessions", "agentpack.example", "https://evil.example", gateway.local.Secret, 403)
	r := httptest.NewRequest("POST", "https://agentpack.example/api/setup", bytes.NewBufferString(`{"password":"long-password-123","token":"`+auth.setup+`"}`))
	r.Header.Set("Origin", "https://agentpack.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "https://agentpack.example/api/sessions", nil)
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), gateway.local.Secret) {
		t.Fatal("authenticated session list", w.Code, w.Body.String())
	}
}

func TestGatewayRestrictsRemoteFolders(t *testing.T) {
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	called := false
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home, Start: func(context.Context, SessionInfo) (string, error) { called = true; return "session-test", nil }}}
	r := httptest.NewRequest("POST", origin.String()+"/api/sessions", bytes.NewBufferString(`{"directory":"/unregistered","harness":"claude"}`))
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || called {
		t.Fatal("unregistered folder launched", w.Code)
	}
}
