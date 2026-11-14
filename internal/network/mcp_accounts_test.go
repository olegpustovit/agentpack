package network

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthCredentialsFollowAccountRevocation(t *testing.T) {
	h := newOAuthHarness(t)
	tokens := h.exchange(t, h.codeForm(h.code(t)), 200)
	pendingCode := h.code(t)
	request := func(token string) *http.Request {
		r := httptest.NewRequest("POST", h.server.URL+"/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	verified := h.gateway.mcp.authenticate(request(tokens.Access))
	if verified == nil {
		t.Fatal("valid OAuth token rejected")
	}
	actor, _ := actorFrom(verified.Context())
	if actor.User != testBrowserEmail || actor.Kind != "mcp" {
		t.Fatalf("wrong OAuth actor: %+v", actor)
	}
	w := authRequest(h.gateway.auth, "logout-all", "", h.cookie)
	if w.Code != 204 {
		t.Fatalf("sign out everywhere: %d %s", w.Code, w.Body.String())
	}
	if h.gateway.mcp.authenticate(request(tokens.Access)) != nil {
		t.Fatal("access token survived account revocation")
	}
	h.exchange(t, h.codeForm(pendingCode), 400)
	h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.Refresh}, "client_id": {h.clientID}}, 400)
	h.cookie = &http.Cookie{Name: h.gateway.auth.cookieName(), Value: randomToken()}
	testBrowserSession(t, h.gateway.auth, "other@example.com", h.cookie.Value)
	other := h.exchange(t, h.codeForm(h.code(t)), 200)
	if h.gateway.mcp.authenticate(request(other.Access)) == nil {
		t.Fatal("revocation crossed account identities")
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE users SET disabled=1 WHERE email='other@example.com'"); err != nil {
		t.Fatal(err)
	}
	if h.gateway.mcp.authenticate(request(other.Access)) != nil {
		t.Fatal("disabled OAuth account accepted")
	}
}

func TestMCPResponseBodyIsByteBounded(t *testing.T) {
	w := &mcpResponse{header: make(http.Header)}
	chunk := make([]byte, 1024)
	for range 4096 {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("premature limit: %d %v", n, err)
		}
	}
	if n, err := w.Write([]byte{1}); err == nil || n != 0 || !w.tooLarge || w.body.Len() != 4<<20 {
		t.Fatalf("response unbounded: %d %v %d", n, err, w.body.Len())
	}
	if w.status != http.StatusOK {
		t.Fatalf("default response status: %d", w.status)
	}
}
