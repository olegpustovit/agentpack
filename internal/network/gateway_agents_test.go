//go:build linux || darwin

package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/session"
)

// Sign-in routes are the owner's, read each agent's account, and drive one login per agent.
func TestAgentRoutesSignInForTheOwnerOnly(t *testing.T) {
	home := t.TempDir()
	saved := agents
	t.Cleanup(func() { agents = saved })
	probes := 0
	loggedOut := false
	agents = []registry.Entry{{Harness: harness.Harness{Target: harness.Codex}, Login: &harness.LoginSpec{Methods: []harness.LoginMethod{{ID: "chatgpt", Label: "ChatGPT account"}}, Account: func(context.Context) harness.Account {
		probes++
		return harness.Account{Installed: true}
	}, Login: func(string) (harness.LoginFlow, error) {
		return harness.LoginFlow{Command: loginChild(t, "AGENTPACK_TEST_LOGIN_HANG=1"), Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}}, Input: "code"}, nil
	}, Logout: func(context.Context) error {
		loggedOut = true
		return nil
	}}}}
	g, origin := identityGateway(t, home, nil)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	handler := g.Handler()
	call := func(method, path, body, token string) (int, string) {
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := call("GET", "/api/agents", "", "member"); code != 200 || !strings.Contains(body, `"harness":"codex"`) || !strings.Contains(body, `"signed_in":false`) || !strings.Contains(body, `"id":"chatgpt"`) {
		t.Fatal("accounts", code, body)
	}
	call("GET", "/api/agents", "", "member")
	if probes != 1 {
		t.Fatalf("accounts probed %d times within the cache window", probes)
	}
	if code, _ := call("POST", "/api/agents/codex/logout", "", "member"); code != http.StatusForbidden || loggedOut {
		t.Fatal("a member signed an agent out", code)
	}
	if code, body := call("POST", "/api/agents/codex/logout", "", "owner"); code != 200 || !loggedOut || !strings.Contains(body, `"signed_out":true`) {
		t.Fatal("sign out", code, body)
	}
	if call("GET", "/api/agents", "", "member"); probes != 2 {
		t.Fatalf("accounts not probed again after a sign-out: %d", probes)
	}
	if code, _ := call("POST", "/api/agents/codex/login", `{"method":"chatgpt"}`, "member"); code != http.StatusForbidden {
		t.Fatal("a member started a sign-in", code)
	}
	code, body := call("POST", "/api/agents/codex/login", `{"method":"chatgpt"}`, "owner")
	var step loginStep
	if code != 200 || json.Unmarshal([]byte(body), &step) != nil || step.URL != "https://auth.example/device?x=1" || step.State != "waiting" {
		t.Fatal("start", code, body)
	}
	if code, _ := call("GET", "/api/agents/codex/login/"+step.ID, "", "member"); code != http.StatusForbidden {
		t.Fatal("a member read the sign-in link", code)
	}
	if code, _ := call("DELETE", "/api/agents/codex/login/"+step.ID, "", "owner"); code != http.StatusNoContent {
		t.Fatal("cancel", code)
	}
	attempt := g.agents.attempt("codex", step.ID)
	<-attempt.done
	if code, body := call("GET", "/api/agents/codex/login/"+step.ID, "", "owner"); code != 200 || !strings.Contains(body, `"state":"failed"`) {
		t.Fatal("cancelled sign-in", code, body)
	}
}

func TestRequireOwnerActors(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.store.DB.Exec("INSERT INTO users(email, password_hash, role, created_at) VALUES ('member@example.com','hash','member',2) ON CONFLICT(email) DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth}
	for _, tc := range []struct {
		name  string
		actor session.Actor
		want  bool
	}{
		{"browser owner", session.Actor{Kind: "browser", User: "owner@example.com"}, true},
		{"browser member", session.Actor{Kind: "browser", User: "member@example.com"}, false},
		{"local owner", session.Actor{Kind: "local", Account: true, User: "owner@example.com"}, true},
		{"local member", session.Actor{Kind: "local", Account: true, User: "member@example.com"}, false},
		{"bare peer", session.Actor{Kind: "peer", User: "owner@example.com"}, false},
		{"forwarded owner", session.Actor{Kind: "peer", Account: true, User: "owner@example.com"}, true},
		{"forwarded member", session.Actor{Kind: "peer", Account: true, User: "member@example.com"}, false},
		{"MCP owner", session.Actor{Kind: "mcp", User: "owner@example.com"}, true},
		{"MCP member", session.Actor{Kind: "mcp", User: "member@example.com"}, false},
		{"unbound local with accounts", session.Actor{Kind: "local", User: "native"}, false},
		{"unknown", session.Actor{Kind: "unknown", User: "owner@example.com"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			w := httptest.NewRecorder()
			if got := g.requireOwner(w, r.WithContext(session.WithActor(r.Context(), tc.actor))); got != tc.want {
				t.Fatalf("owner access = %v, want %v", got, tc.want)
			}
		})
	}
}
