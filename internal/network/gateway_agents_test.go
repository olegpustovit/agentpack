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
)

// Sign-in routes are the owner's, read each agent's account, and drive one login per agent.
func TestAgentRoutesSignInForTheOwnerOnly(t *testing.T) {
	home := t.TempDir()
	saved := agentLogins
	t.Cleanup(func() { agentLogins = saved })
	probes := 0
	loggedOut := false
	agentLogins = []agentLogin{{"codex", []harness.LoginMethod{{ID: "chatgpt", Label: "ChatGPT account"}}, func(context.Context) harness.Account {
		probes++
		return harness.Account{Installed: true}
	}, func(string) (harness.LoginFlow, error) {
		return harness.LoginFlow{Command: loginChild(t, "AGENTPACK_TEST_LOGIN_HANG=1"), Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}}, Input: "code"}, nil
	}, func(context.Context) error {
		loggedOut = true
		return nil
	}}}
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
