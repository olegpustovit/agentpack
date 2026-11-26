//go:build linux || darwin

package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

// Sign-in routes are the owner's, read each agent's account, and drive one login per agent.
func TestAgentRoutesSignInForTheOwnerOnly(t *testing.T) {
	home := t.TempDir()
	saved := network.Agents
	t.Cleanup(func() { network.Agents = saved })
	probes := 0
	loggedOut := false
	network.Agents = []registry.Entry{{Harness: harness.Harness{Target: harness.Codex}, Login: &harness.LoginSpec{Methods: []harness.LoginMethod{{ID: "chatgpt", Label: "ChatGPT account"}}, Account: func(context.Context) harness.Account {
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

// The shared sign-in list is the owner's, carries no sign-in bytes, and marks a sign-in expired by
// its own lifetime or by a failed renewal of the current revision.
func TestSharedAgentsReportsMetadataOnly(t *testing.T) {
	home := t.TempDir()
	saved := network.Agents
	t.Cleanup(func() { network.Agents = saved })
	expiry := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	failing := errors.New("invalid_grant")
	file := func(name string) func() string {
		return func() string { return filepath.Join(home, name, "auth.json") }
	}
	network.Agents = []registry.Entry{
		{Harness: harness.Harness{Target: harness.Claude}, Login: &harness.LoginSpec{Credential: file("claude"), FreshnessExpires: true, Freshness: func([]byte) time.Time { return expiry },
			Refresh: func(context.Context, []byte) ([]byte, error) { return nil, failing }}},
		{Harness: harness.Harness{Target: harness.Codex}, Login: &harness.LoginSpec{Credential: file("codex"), Freshness: func([]byte) time.Time { return time.Unix(1, 0) },
			Refresh: func(context.Context, []byte) ([]byte, error) { return nil, nil }}},
		{Harness: harness.Harness{Target: harness.Cursor}, Login: &harness.LoginSpec{Credential: file("cursor"), FreshnessExpires: true, Freshness: func([]byte) time.Time { return time.Unix(1, 0) }}},
		{Harness: harness.Harness{Target: harness.Grok}, Login: &harness.LoginSpec{Credential: file("grok")}},
		{Harness: harness.Harness{Target: harness.Agy}, Login: &harness.LoginSpec{Credential: file("agy")}},
		{Harness: harness.Harness{Target: harness.OMP}, Login: &harness.LoginSpec{}},
	}
	g, origin := identityGateway(t, home, nil)
	testBrowserSession(t, g.auth, testBrowserEmail, "owner")
	testBrowserSession(t, g.auth, "member@example.com", "member")
	ctx := context.Background()
	node, err := g.store.NodeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const token = "fixture-token-5f3a9c"
	data := []byte(`{"access_token":"` + token + `"}`)
	for key, credential := range map[string]sharedAgentCredential{
		"claude/auth.json": {Data: data, Rev: 1, Modified: 1000, Node: node},
		"codex/auth.json":  {Data: data, Rev: 1, Modified: 1000, Node: node},
		"cursor/auth.json": {Data: data, Rev: 1, Modified: 1000, Node: node},
		"grok/auth.json":   {Rev: 2, Modified: 1000},
	} {
		if err := g.publishAgentCredential(ctx, key, credential); err != nil {
			t.Fatal(err)
		}
	}
	handler := g.Handler()
	call := func(session string) (int, string, map[string]sharedSignIn) {
		r := httptest.NewRequest("GET", origin.String()+"/api/agents/shared", nil)
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: session})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var reply struct{ Agents []sharedSignIn }
		_ = json.Unmarshal(w.Body.Bytes(), &reply)
		rows := map[string]sharedSignIn{}
		for _, row := range reply.Agents {
			rows[row.Harness] = row
		}
		return w.Code, w.Body.String(), rows
	}
	if code, body, _ := call("member"); code != http.StatusForbidden || strings.Contains(body, "harness") {
		t.Fatal("a member read the shared sign-ins", code, body)
	}
	code, body, rows := call("owner")
	// The file holds the token in base64, as the row does; neither form may leave the gateway.
	if code != 200 || strings.Contains(body, token) || strings.Contains(body, base64.StdEncoding.EncodeToString(data)) || strings.Contains(body, `"data"`) {
		t.Fatal("shared sign-ins", code, body)
	}
	if got := rows["claude"]; got.State != "shared" || !got.ExpiresAt.Equal(expiry) || got.RenewerNode != node || got.PublisherNode != node || got.UpdatedAt.UnixMilli() != 1000 {
		t.Fatalf("claude: %+v", got)
	}
	// Codex's Freshness is when it last renewed, so it is not an expiry.
	if got := rows["codex"]; got.State != "shared" || !got.ExpiresAt.IsZero() || got.RenewerNode != node {
		t.Fatalf("codex: %+v", got)
	}
	if got := rows["cursor"]; got.State != "expired" || got.RenewerNode != "" {
		t.Fatalf("expired by time: %+v", got)
	}
	if rows["grok"].State != "stopped" || rows["agy"].State != "none" || len(rows) != 5 {
		t.Fatalf("rows: %+v", rows)
	}

	// This gateway holds the published file, so its next round renews it and fails.
	claude := network.Agents[0]
	if err := os.MkdirAll(filepath.Dir(claude.Login.Credential()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude.Login.Credential(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.syncAgentCredential(ctx, claude, claude.Login.Credential(), node, false); !errors.Is(err, failing) {
		t.Fatal("renewal", err)
	}
	if _, _, rows := call("owner"); rows["claude"].State != "expired" || rows["claude"].RefreshErrorAt.IsZero() {
		t.Fatalf("expired by refresh failure: %+v", rows["claude"])
	}
	failing = nil
	if _, err := g.syncAgentCredential(ctx, claude, claude.Login.Credential(), node, false); err != nil {
		t.Fatal(err)
	}
	if _, _, rows := call("owner"); rows["claude"].State != "shared" || !rows["claude"].RefreshErrorAt.IsZero() {
		t.Fatalf("a later success did not clear the failure: %+v", rows["claude"])
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
