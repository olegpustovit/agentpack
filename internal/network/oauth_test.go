package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	bolt "go.etcd.io/bbolt"
)

type oauthHarness struct {
	gateway  *Gateway
	server   *httptest.Server
	cookie   *http.Cookie
	clientID string
	verifier string
}

func newOAuthHarness(t *testing.T) *oauthHarness {
	t.Helper()
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "agentpack_session", Value: randomToken()}
	auth.sessions[sha256.Sum256([]byte(cookie.Value))] = time.Now().Add(time.Hour)
	server := httptest.NewUnstartedServer(nil)
	origin, _ := url.Parse("http://" + server.Listener.Addr().String())
	g := &Gateway{options: GatewayOptions{Home: home}, auth: auth, origin: origin, local: nodeRecord{Address: server.Listener.Addr().String(), Secret: randomToken()}}
	g.mcp, err = NewGatewayMCP(g)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = g.Handler()
	server.Start()
	t.Cleanup(func() { server.Close(); _ = g.mcp.Close() })
	h := &oauthHarness{gateway: g, server: server, cookie: cookie, verifier: strings.Repeat("a", 43)}
	response, data := h.request(t, "POST", "/oauth/register", `{"client_name":"Test assistant","redirect_uris":["https://assistant.example/callback"],"token_endpoint_auth_method":"none"}`, "application/json", nil, false)
	if response.StatusCode != 201 {
		t.Fatalf("register: %d %s", response.StatusCode, data)
	}
	var result struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	h.clientID = result.ClientID
	return h
}

func (h *oauthHarness) request(t *testing.T, method, path, body, contentType string, cookie *http.Cookie, origin bool) (*http.Response, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin {
		r.Header.Set("Origin", h.server.URL)
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func (h *oauthHarness) authorizePath() string {
	hash := sha256.Sum256([]byte(h.verifier))
	query := url.Values{"client_id": {h.clientID}, "redirect_uri": {"https://assistant.example/callback"}, "response_type": {"code"}, "scope": {"agentpack"}, "state": {"state-value"}, "resource": {h.server.URL + "/mcp"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
	return "/oauth/authorize?" + query.Encode()
}

func (h *oauthHarness) consent(t *testing.T) (string, string) {
	t.Helper()
	path := h.authorizePath()
	response, data := h.request(t, "GET", path, "", "", h.cookie, false)
	if response.StatusCode != 200 {
		t.Fatalf("consent: %d %s", response.StatusCode, data)
	}
	_, after, ok := strings.Cut(string(data), `name="consent" value="`)
	if !ok {
		t.Fatal("consent missing")
	}
	token, _, ok := strings.Cut(after, `"`)
	if !ok {
		t.Fatal("consent value missing")
	}
	return path, token
}

func (h *oauthHarness) code(t *testing.T) string {
	t.Helper()
	path, consent := h.consent(t)
	response, data := h.request(t, "POST", path, url.Values{"consent": {consent}, "decision": {"allow"}}.Encode(), "application/x-www-form-urlencoded", h.cookie, true)
	if response.StatusCode != 303 {
		t.Fatalf("authorize: %d %s", response.StatusCode, data)
	}
	redirect, err := url.Parse(response.Header.Get("Location"))
	if err != nil || redirect.Host != "assistant.example" || redirect.Query().Get("state") != "state-value" || redirect.Query().Get("code") == "" {
		t.Fatalf("redirect: %s %v", redirect, err)
	}
	return redirect.Query().Get("code")
}

type oauthTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

func (h *oauthHarness) exchange(t *testing.T, form url.Values, status int) oauthTokens {
	t.Helper()
	response, data := h.request(t, "POST", "/oauth/token", form.Encode(), "application/x-www-form-urlencoded", nil, false)
	if response.StatusCode != status {
		t.Fatalf("token: want %d got %d %s", status, response.StatusCode, data)
	}
	var tokens oauthTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens
}

func (h *oauthHarness) codeForm(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {h.clientID}, "redirect_uri": {"https://assistant.example/callback"}, "code": {code}, "code_verifier": {h.verifier}, "resource": {h.server.URL + "/mcp"}}
}

func TestOAuthConsentPKCEBindingAndRefreshReplay(t *testing.T) {
	h := newOAuthHarness(t)
	response, _ := h.request(t, "POST", "/mcp", "{}", "application/json", nil, false)
	if response.StatusCode != 401 || !strings.Contains(response.Header.Get("WWW-Authenticate"), "oauth-protected-resource/mcp") {
		t.Fatal("missing OAuth challenge", response.StatusCode)
	}
	response, data := h.request(t, "GET", "/.well-known/oauth-authorization-server/mcp", "", "", nil, false)
	if response.StatusCode != 200 || !bytes.Contains(data, []byte(`"S256"`)) {
		t.Fatal("missing metadata", string(data))
	}
	response, _ = h.request(t, "POST", "/oauth/register", `{"redirect_uris":["http://evil.example/callback"]}`, "application/json", nil, false)
	if response.StatusCode != 400 {
		t.Fatal("insecure redirect accepted")
	}
	path, consent := h.consent(t)
	form := url.Values{"consent": {consent}, "decision": {"allow"}}
	response, _ = h.request(t, "POST", path, form.Encode(), "application/x-www-form-urlencoded", h.cookie, false)
	if response.StatusCode != 403 {
		t.Fatal("cross-site consent accepted")
	}
	other := &http.Cookie{Name: h.cookie.Name, Value: randomToken()}
	h.gateway.auth.mu.Lock()
	h.gateway.auth.sessions[sha256.Sum256([]byte(other.Value))] = time.Now().Add(time.Hour)
	h.gateway.auth.mu.Unlock()
	response, _ = h.request(t, "POST", path, form.Encode(), "application/x-www-form-urlencoded", other, true)
	if response.StatusCode != 400 {
		t.Fatal("consent crossed browser sessions")
	}
	code := h.code(t)
	grant := h.codeForm(code)
	grant.Set("resource", "https://other.example/mcp")
	h.exchange(t, grant, 400)
	grant.Set("resource", h.server.URL+"/mcp")
	grant.Set("client_id", "wrong-client")
	h.exchange(t, grant, 400)
	grant.Set("client_id", h.clientID)
	grant.Set("redirect_uri", "https://assistant.example/other")
	h.exchange(t, grant, 400)
	grant.Set("redirect_uri", "https://assistant.example/callback")
	grant.Set("code_verifier", strings.Repeat("b", 43))
	h.exchange(t, grant, 400)
	grant.Set("code_verifier", h.verifier)
	first := h.exchange(t, grant, 200)
	h.exchange(t, grant, 400)
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {h.clientID}, "refresh_token": {first.Refresh}, "resource": {h.server.URL + "/mcp"}}
	second := h.exchange(t, refresh, 200)
	if second.Access == first.Access || second.Refresh == first.Refresh {
		t.Fatal("tokens did not rotate")
	}
	if err := h.gateway.mcp.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.gateway.mcp.db, err = openOAuth(h.gateway.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", h.server.URL+"/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+second.Access)
	if !h.gateway.mcp.authenticated(r) {
		t.Fatal("token did not survive restart")
	}
	h.exchange(t, refresh, 400)
	if h.gateway.mcp.authenticated(r) {
		t.Fatal("refresh replay did not revoke family")
	}
	third := h.exchange(t, h.codeForm(h.code(t)), 200)
	h.request(t, "POST", "/oauth/revoke", url.Values{"token": {third.Refresh}, "client_id": {h.clientID}}.Encode(), "application/x-www-form-urlencoded", nil, false)
	r.Header.Set("Authorization", "Bearer "+third.Access)
	if h.gateway.mcp.authenticated(r) {
		t.Fatal("revocation left access token valid")
	}
}

type mcpBearerTransport struct{ token string }

func (transport mcpBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(request)
}

func TestMCPAuthenticatedSDKToolsReuseGatewayPolicy(t *testing.T) {
	h := newOAuthHarness(t)
	tokens := h.exchange(t, h.codeForm(h.code(t)), 200)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.server.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpBearerTransport{tokens.Access}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"install_package", "list_nodes", "list_packages", "list_recent_folders", "list_sessions", "read_terminal", "restart_session", "start_session", "stop_session", "write_terminal"}
	if !slices.Equal(names, want) {
		t.Fatal("tool inventory", names)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_sessions", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("list sessions: %v %+v", err, result)
	}
	var started atomic.Int32
	h.gateway.options.Start = func(context.Context, SessionInfo) (string, error) { started.Add(1); return "session-test", nil }
	directory := filepath.Join(h.gateway.options.Home, "project")
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "harness": "codex"}})
	if err != nil || !result.IsError || started.Load() != 0 {
		t.Fatalf("unknown folder accepted: %v %+v", err, result)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(h.gateway.options.Home, SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "harness": "codex"}})
	if err != nil || result.IsError || started.Load() != 1 {
		t.Fatalf("known folder failed: %v %+v", err, result)
	}
	restartIDs := make(chan string, 2)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/restart" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			http.Error(w, "invalid worker request", 400)
			return
		}
		restartIDs <- r.Header.Get("Idempotency-Key")
		writeJSON(w, map[string]bool{"restarted": true})
	}))
	defer worker.Close()
	record := WorkerRecord{ID: "session-restart", Secret: strings.Repeat("a", 64), Address: strings.TrimPrefix(worker.URL, "http://")}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	workerDirectory := filepath.Join(h.gateway.options.Home, "network", "sessions", record.ID)
	if err := os.MkdirAll(workerDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDirectory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "restart_session", Arguments: map[string]any{"session_id": record.ID}})
	if err == nil && !result.IsError {
		t.Fatal("restart accepted a missing operation ID")
	}
	for range 2 {
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "restart_session", Arguments: map[string]any{"session_id": record.ID, "operation_id": "restart-operation-123"}})
		if err != nil || result.IsError {
			t.Fatalf("restart with operation ID: %v %+v", err, result)
		}
	}
	if len(restartIDs) != 2 || <-restartIDs != "restart-operation-123" || <-restartIDs != "restart-operation-123" {
		t.Fatal("MCP did not preserve restart operation IDs")
	}
}

func TestOAuthRegisteredClientDoesNotExpire(t *testing.T) {
	h := newOAuthHarness(t)
	err := h.gateway.mcp.db.Update(func(tx *bolt.Tx) error {
		client, err := oauthGet(tx, h.clientID)
		if err != nil {
			return err
		}
		if !client.Expires.IsZero() {
			t.Fatal("registered client has a time limit")
		}
		// Only credentials expire: clients registered once must survive pruning.
		if err := oauthPut(tx, "expired-access", oauthRecord{Kind: "access", Expires: time.Now().Add(-time.Hour)}); err != nil {
			return err
		}
		if err := oauthPrune(tx); err != nil {
			return err
		}
		if _, err := oauthGet(tx, "expired-access"); err == nil {
			t.Fatal("expired access token survived")
		}
		_, err = oauthGet(tx, h.clientID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOAuthAuthorizeWithoutSessionRedirectsToLogin(t *testing.T) {
	h := newOAuthHarness(t)
	path := h.authorizePath()
	response, data := h.request(t, "GET", path, "", "", nil, false)
	if want := "/login?return_to=" + url.QueryEscape(path); response.StatusCode != 303 || response.Header.Get("Location") != want {
		t.Fatalf("redirect: want 303 %s got %d %q %s", want, response.StatusCode, response.Header.Get("Location"), data)
	}
}
