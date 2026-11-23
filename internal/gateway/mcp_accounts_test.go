package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
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
	verified := h.gateway.mcp.Authenticate(request(tokens.Access))
	if verified == nil {
		t.Fatal("valid OAuth token rejected")
	}
	actor, _ := session.ActorFrom(verified.Context())
	if actor.User != testBrowserEmail || actor.Kind != "mcp" {
		t.Fatalf("wrong OAuth actor: %+v", actor)
	}
	w := authRequest(h.gateway.auth, "logout-all", "", h.cookie)
	if w.Code != 204 {
		t.Fatalf("sign out everywhere: %d %s", w.Code, w.Body.String())
	}
	if h.gateway.mcp.Authenticate(request(tokens.Access)) != nil {
		t.Fatal("access token survived account revocation")
	}
	h.exchange(t, h.codeForm(pendingCode), 400)
	h.exchange(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.Refresh}, "client_id": {h.clientID}}, 400)
	h.cookie = &http.Cookie{Name: "agentpack_session", Value: network.RandomToken()}
	testBrowserSession(t, h.gateway.auth, "other@example.com", h.cookie.Value)
	other := h.exchange(t, h.codeForm(h.code(t)), 200)
	if h.gateway.mcp.Authenticate(request(other.Access)) == nil {
		t.Fatal("revocation crossed account identities")
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE users SET disabled=1 WHERE email='other@example.com'"); err != nil {
		t.Fatal(err)
	}
	if h.gateway.mcp.Authenticate(request(other.Access)) != nil {
		t.Fatal("disabled OAuth account accepted")
	}
}
