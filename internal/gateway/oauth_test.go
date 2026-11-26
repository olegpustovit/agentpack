package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gatewaymcp "github.com/olegpustovit/agentpack/internal/gateway/mcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

type oauthHarness struct {
	gateway  *Gateway
	server   *httptest.Server
	issuer   string
	cookie   *http.Cookie
	clientID string
	verifier string
}

func newOAuthHarness(t *testing.T) *oauthHarness {
	t.Helper()
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "agentpack_session", Value: network.RandomToken()}
	testBrowserSession(t, auth, testBrowserEmail, cookie.Value)
	server := httptest.NewUnstartedServer(nil)
	origin, _ := url.Parse("http://" + server.Listener.Addr().String())
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}, auth: auth, origin: origin, local: network.NodeRecord{Address: server.Listener.Addr().String(), Secret: network.RandomToken()}}
	g.mcp, err = gatewaymcp.New(store, home, origin.String(), g)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = g.Handler()
	server.Start()
	t.Cleanup(func() { server.Close(); _ = g.store.Close() })
	h := &oauthHarness{gateway: g, server: server, issuer: server.URL, cookie: cookie, verifier: strings.Repeat("a", 43)}
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
		r.Header.Set("Origin", h.issuer)
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
	query := url.Values{"client_id": {h.clientID}, "redirect_uri": {"https://assistant.example/callback"}, "response_type": {"code"}, "scope": {"agentpack"}, "state": {"state-value"}, "resource": {h.issuer + "/mcp"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
	return "/oauth/authorize?" + query.Encode()
}

func (h *oauthHarness) consent(t *testing.T) (string, string) {
	t.Helper()
	path := h.authorizePath()
	response, data := h.request(t, "GET", path, "", "", h.cookie, false)
	if response.StatusCode != 200 {
		t.Fatalf("consent: %d %s", response.StatusCode, data)
	}
	if policy := response.Header.Get("Referrer-Policy"); policy != "same-origin" {
		t.Fatalf("consent Referrer-Policy = %q, want same-origin for the form POST", policy)
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
	if policy := response.Header.Get("Referrer-Policy"); policy != "no-referrer" {
		t.Fatalf("callback redirect Referrer-Policy = %q, want no-referrer", policy)
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
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {h.clientID}, "redirect_uri": {"https://assistant.example/callback"}, "code": {code}, "code_verifier": {h.verifier}, "resource": {h.issuer + "/mcp"}}
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
		t.Fatal("consent accepted without an Origin header")
	}
	for _, origin := range []string{"null", "https://assistant.example"} {
		w := serve(h.gateway.Handler(), "POST", h.server.URL+path, "127.0.0.1:5000", map[string]string{"Origin": origin, "Cookie": h.cookie.String()})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"invalid_origin"`) {
			t.Fatalf("consent Origin %q: %d %s", origin, w.Code, w.Body.String())
		}
	}
	other := &http.Cookie{Name: h.cookie.Name, Value: network.RandomToken()}
	testBrowserSession(t, h.gateway.auth, testBrowserEmail, other.Value)
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
	if err := h.gateway.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.gateway.store, err = state.Open(h.gateway.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	if h.gateway.mcp, err = gatewaymcp.New(h.gateway.store, h.gateway.options.Home, h.gateway.origin.String(), h.gateway); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", h.server.URL+"/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+second.Access)
	if h.gateway.mcp.Authenticate(r) == nil {
		t.Fatal("token did not survive restart")
	}
	h.exchange(t, refresh, 400)
	if h.gateway.mcp.Authenticate(r) != nil {
		t.Fatal("refresh replay did not revoke family")
	}
	third := h.exchange(t, h.codeForm(h.code(t)), 200)
	h.request(t, "POST", "/oauth/revoke", url.Values{"token": {third.Refresh}, "client_id": {h.clientID}}.Encode(), "application/x-www-form-urlencoded", nil, false)
	r.Header.Set("Authorization", "Bearer "+third.Access)
	if h.gateway.mcp.Authenticate(r) != nil {
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
	backend, _ := url.Parse(h.server.URL)
	public, err := parsePublicOrigin(testPublicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	h.gateway.public.mu.Lock()
	h.gateway.public.origin = public
	h.gateway.public.mu.Unlock()
	// Simulate HTTPS terminating at a loopback proxy that preserves the public Host.
	h.server = httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backend)
		r.Out.Host = public.Host
		r.Out.Header.Set("X-Forwarded-Host", public.Host)
		r.Out.Header.Set("X-Forwarded-Proto", "https")
	}})
	t.Cleanup(h.server.Close)
	h.issuer = testPublicOrigin
	h.cookie.Name = "__Host-agentpack_session"
	h.cookie.Value = network.RandomToken()
	const member = "member@example.com"
	testBrowserSession(t, h.gateway.auth, member, h.cookie.Value)

	tokens := h.exchange(t, h.codeForm(h.code(t)), 200)
	if response, _ := h.request(t, "POST", "/mcp", "{}", "application/json", nil, false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("public MCP without a token: %d", response.StatusCode)
	}
	bearer := "Bearer " + tokens.Access
	if w := serve(h.gateway.Handler(), "POST", "https://invalid.example/mcp", "127.0.0.1:5000", map[string]string{"Authorization": bearer}); w.Code != http.StatusForbidden {
		t.Fatalf("unknown host reached public MCP: %d", w.Code)
	}
	if w := serve(h.gateway.Handler(), "POST", h.issuer+"/mcp", "127.0.0.1:5000", map[string]string{"Authorization": bearer, "Origin": "https://assistant.example"}); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "invalid_origin") {
		t.Fatalf("cross-origin request reached public MCP: %d %s", w.Code, w.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.server.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpBearerTransport{tokens.Access}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"answer_question", "ask", "cancel", "cancel_task", "fork_session", "get_attention", "get_issue", "get_limits", "get_trace", "get_usage", "install_package", "list_chat_options", "list_issues", "list_nodes", "list_packages", "list_recent_folders", "list_secrets", "list_sessions", "read_conversation", "read_terminal", "reply", "resolve_issue", "respond_permission", "restart_session", "run_helper", "send_prompt", "show", "start_session", "stop_session", "tasks", "wait", "write_terminal"}
	if !slices.Equal(names, want) {
		t.Fatal("tool inventory", names)
	}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_sessions", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("list sessions: %v %+v", err, result)
	}
	var started atomic.Int32
	h.gateway.options.Start = func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		if info.StartedBy == nil || info.StartedBy.Kind != "mcp" || info.StartedBy.User != member {
			t.Errorf("MCP identity lost: %+v", info.StartedBy)
		}
		started.Add(1)
		return "session-test", nil
	}
	directory := filepath.Join(h.gateway.options.Home, "project")
	result, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "agent": "codex", "terminal": true}})
	if err != nil || !result.IsError || started.Load() != 0 {
		t.Fatalf("unknown folder accepted: %v %+v", err, result)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(h.gateway.options.Home, session.SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	result, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "agent": "codex", "terminal": true}})
	if err != nil || result.IsError || started.Load() != 1 {
		t.Fatalf("known folder failed: %v %+v", err, result)
	}
	const spec = "owner/repo"
	var installed atomic.Bool
	h.gateway.options.Install = func(_ context.Context, dir, pkg string, restart bool, _ string) error {
		if dir != directory || pkg != spec || !restart {
			return fmt.Errorf("unexpected package install: %q %q %t", dir, pkg, restart)
		}
		installed.Store(true)
		return nil
	}
	result, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "install_package", Arguments: map[string]any{"directory": directory, "spec": spec}})
	if err != nil || result.IsError || !installed.Load() {
		t.Fatalf("package install as a member: %v %+v", err, result)
	}
	restartIDs := make(chan string, 2)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/restart" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			http.Error(w, "invalid worker request", 400)
			return
		}
		restartIDs <- r.Header.Get("Idempotency-Key")
		network.WriteJSON(w, map[string]bool{"restarted": true})
	}))
	defer worker.Close()
	record := network.WorkerRecord{ID: "session-restart", Secret: strings.Repeat("a", 64), Address: strings.TrimPrefix(worker.URL, "http://")}
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
	if err := session.UpsertSession(t.Context(), h.gateway.store.DB, session.SessionInfo{ID: record.ID}); err != nil {
		t.Fatal(err)
	}
	result, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "restart_session", Arguments: map[string]any{"session_id": record.ID}})
	if err == nil && !result.IsError {
		t.Fatal("restart accepted a missing operation ID")
	}
	for range 2 {
		result, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "restart_session", Arguments: map[string]any{"session_id": record.ID, "operation_id": "restart-operation-123"}})
		if err != nil || result.IsError {
			t.Fatalf("restart with operation ID: %v %+v", err, result)
		}
	}
	if len(restartIDs) != 2 || <-restartIDs != "restart-operation-123" || <-restartIDs != "restart-operation-123" {
		t.Fatal("MCP did not preserve restart operation IDs")
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

func TestOAuthConsentShowsAccessAndRecoversFromExpiry(t *testing.T) {
	h := newOAuthHarness(t)
	path := h.authorizePath()
	response, data := h.request(t, "GET", path, "", "", h.cookie, false)
	body := string(data)
	if response.StatusCode != http.StatusOK || strings.Count(body, "<li>") != 4 {
		t.Fatalf("consent: %d %s", response.StatusCode, body)
	}
	for _, want := range []string{"Test assistant", "assistant.example", session.LocalHostname(), testBrowserEmail, "Settings › Account", "Sign out everywhere"} {
		if !strings.Contains(body, html.EscapeString(want)) {
			t.Fatalf("consent missing %q", want)
		}
	}
	cancel, allow := strings.Index(body, `value="deny">Cancel`), strings.Index(body, `value="allow">Allow access`)
	if cancel < 0 || allow < cancel {
		t.Fatal("consent must put Cancel first")
	}
	_, after, _ := strings.Cut(body, `name="consent" value="`)
	token, _, _ := strings.Cut(after, `"`)
	if token == "" {
		t.Fatal("missing consent token")
	}
	if _, err := h.gateway.store.DB.Exec("UPDATE oauth_tokens SET expires_at=0 WHERE kind='consent'"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"consent": {token}, "decision": {"allow"}}
	response, data = h.request(t, "POST", path, form.Encode(), "application/x-www-form-urlencoded", h.cookie, true)
	body = string(data)
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Location") != "" ||
		!strings.Contains(body, `href="`+html.EscapeString(path)+`">Restart authorization`) ||
		strings.Contains(body, token) || strings.Contains(body, testBrowserEmail) {
		t.Fatalf("expired consent: %d %s", response.StatusCode, body)
	}
	_, fresh := h.consent(t)
	if fresh == token {
		t.Fatal("restart reused the expired consent")
	}
}

func TestOAuthCodeConsumedOnceAcrossStores(t *testing.T) {
	h := newOAuthHarness(t)
	form := h.codeForm(h.code(t)).Encode()
	other, err := state.Open(h.gateway.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second := &Gateway{store: other, options: h.gateway.options, auth: h.gateway.auth, origin: h.gateway.origin, local: h.gateway.local}
	if second.mcp, err = gatewaymcp.New(other, h.gateway.options.Home, h.gateway.origin.String(), second); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, g := range []*Gateway{h.gateway, second} {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, h.server.URL+"/oauth/token", strings.NewReader(form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.RemoteAddr = "127.0.0.1:1"
			w := httptest.NewRecorder()
			g.Handler().ServeHTTP(w, r)
			results <- w.Code
		}()
	}
	close(start)
	first, last := <-results, <-results
	if first+last != 600 || first != 200 && first != 400 {
		t.Fatalf("concurrent code exchange statuses: %d, %d", first, last)
	}
}

func (h *oauthHarness) authorizeFor(t *testing.T, clientID, redirect, scope string) (*http.Response, string) {
	t.Helper()
	hash := sha256.Sum256([]byte(h.verifier))
	path := "/oauth/authorize?" + url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {scope}, "state": {"s"}, "resource": {h.server.URL + "/mcp"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}.Encode()
	response, data := h.request(t, "GET", path, "", "", h.cookie, false)
	if response.StatusCode != 200 {
		return response, string(data)
	}
	_, after, _ := strings.Cut(string(data), `name="consent" value="`)
	consent, _, _ := strings.Cut(after, `"`)
	response, _ = h.request(t, "POST", path, url.Values{"consent": {consent}, "decision": {"allow"}}.Encode(), "application/x-www-form-urlencoded", h.cookie, true)
	location, _ := url.Parse(response.Header.Get("Location"))
	return response, location.Query().Get("code")
}

func TestOAuthClientMetadataDocument(t *testing.T) {
	h := newOAuthHarness(t)
	const id = "https://client.example.com/oauth/client.json"
	documents := map[string]string{
		"/oauth/client.json": `{"client_id":"` + id + `","client_name":"Hosted assistant","redirect_uris":["http://127.0.0.1/callback"],"token_endpoint_auth_method":"private_key_jwt"}`,
		"/other.json":        `{"client_id":"https://elsewhere.example.com/client.json","redirect_uris":["https://assistant.example/callback"]}`,
		"/secret.json":       `{"client_id":"https://client.example.com/secret.json","client_secret":"s","redirect_uris":["https://assistant.example/callback"]}`,
		"/big.json":          `{"client_id":"https://client.example.com/big.json","redirect_uris":["https://assistant.example/callback"],"pad":"` + strings.Repeat("x", 16<<10) + `"}`,
	}
	var fetches atomic.Int32
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		document, ok := documents[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(document))
	}))
	defer host.Close()
	client := host.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, host.Listener.Addr().String())
	}
	h.gateway.mcp.Fetcher = client
	_, data := h.request(t, "GET", "/.well-known/oauth-authorization-server", "", "", nil, false)
	if !bytes.Contains(data, []byte(`"client_id_metadata_document_supported":true`)) {
		t.Fatalf("metadata does not advertise CIMD: %s", data)
	}
	// The document's loopback redirect also accepts any port (RFC 8252).
	response, code := h.authorizeFor(t, id, "http://127.0.0.1:43123/callback", "agentpack offline_access")
	if response.StatusCode != 303 || code == "" {
		t.Fatalf("authorize with a metadata document: %d %s", response.StatusCode, code)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "redirect_uri": {"http://127.0.0.1:43123/callback"}, "code": {code}, "code_verifier": {h.verifier}, "resource": {h.server.URL + "/mcp"}, "scope": {"offline_access agentpack"}}
	if tokens := h.exchange(t, form, 200); tokens.Refresh == "" {
		t.Fatal("no refresh token")
	}
	if response, _ := h.authorizeFor(t, id, "http://127.0.0.1:9/callback", ""); response.StatusCode != 303 || fetches.Load() != 1 {
		t.Fatalf("cached document: %d after %d fetches", response.StatusCode, fetches.Load())
	}
	for _, bad := range []string{"https://client.example.com/other.json", "https://client.example.com/secret.json", "https://client.example.com/big.json", "https://client.example.com/", "https://client.example.com/missing.json"} {
		if response, _ := h.authorizeFor(t, bad, "https://assistant.example/callback", ""); response.StatusCode != 400 {
			t.Fatalf("%s accepted: %d", bad, response.StatusCode)
		}
	}
	// Unsigned visitors can trigger fetches; a host gets ten uncached tries a minute, then none.
	before := fetches.Load()
	for i := range 15 {
		h.authorizeFor(t, fmt.Sprintf("https://client.example.com/missing-%d.json", i), "https://assistant.example/callback", "")
	}
	if got := fetches.Load() - before; got > 10 {
		t.Fatalf("%d uncached fetches for one host in a minute", got)
	}
}

func TestOAuthRegistrationScopesRedirectsAndLimiter(t *testing.T) {
	h := newOAuthHarness(t)
	response, data := h.request(t, "POST", "/oauth/register", `{"redirect_uris":["http://localhost/cb","https://assistant.example/callback"],"token_endpoint_auth_method":"client_secret_basic"}`, "application/json", nil, false)
	var registered struct {
		ClientID  string `json:"client_id"`
		TokenAuth string `json:"token_endpoint_auth_method"`
	}
	if response.StatusCode != 201 || json.Unmarshal(data, &registered) != nil || registered.TokenAuth != "none" {
		t.Fatalf("auth method was not downgraded: %d %s", response.StatusCode, data)
	}
	if response, _ := h.authorizeFor(t, registered.ClientID, "http://localhost:51234/cb", "agentpack offline_access"); response.StatusCode != 303 {
		t.Fatalf("loopback port or scope set refused: %d", response.StatusCode)
	}
	for _, test := range []struct{ redirect, scope string }{{"http://localhost:51234/other", ""}, {"https://assistant.example:8443/callback", ""}, {"https://assistant.example/callback", "agentpack admin"}} {
		if response, _ := h.authorizeFor(t, registered.ClientID, test.redirect, test.scope); response.StatusCode != 400 {
			t.Fatalf("%+v accepted: %d", test, response.StatusCode)
		}
	}
}
