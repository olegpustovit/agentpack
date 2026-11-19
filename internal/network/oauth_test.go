package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"github.com/olegpustovit/agentpack/internal/state"
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
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "agentpack_session", Value: randomToken()}
	testBrowserSession(t, auth, testBrowserEmail, cookie.Value)
	server := httptest.NewUnstartedServer(nil)
	origin, _ := url.Parse("http://" + server.Listener.Addr().String())
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: store, options: GatewayOptions{Home: home}, auth: auth, origin: origin, local: nodeRecord{Address: server.Listener.Addr().String(), Secret: randomToken()}}
	g.mcp, err = NewGatewayMCP(g)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = g.Handler()
	server.Start()
	t.Cleanup(func() { server.Close(); _ = g.store.Close() })
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
	r := httptest.NewRequest("POST", h.server.URL+"/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+second.Access)
	if h.gateway.mcp.authenticate(r) == nil {
		t.Fatal("token did not survive restart")
	}
	h.exchange(t, refresh, 400)
	if h.gateway.mcp.authenticate(r) != nil {
		t.Fatal("refresh replay did not revoke family")
	}
	third := h.exchange(t, h.codeForm(h.code(t)), 200)
	h.request(t, "POST", "/oauth/revoke", url.Values{"token": {third.Refresh}, "client_id": {h.clientID}}.Encode(), "application/x-www-form-urlencoded", nil, false)
	r.Header.Set("Authorization", "Bearer "+third.Access)
	if h.gateway.mcp.authenticate(r) != nil {
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
	want := []string{"answer_question", "cancel", "fork_session", "get_attention", "get_issue", "get_limits", "get_trace", "get_usage", "install_package", "list_issues", "list_nodes", "list_packages", "list_recent_folders", "list_sessions", "read_conversation", "read_terminal", "resolve_issue", "respond_permission", "restart_session", "send_prompt", "start_session", "stop_session", "wait", "write_terminal"}
	if !slices.Equal(names, want) {
		t.Fatal("tool inventory", names)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_sessions", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("list sessions: %v %+v", err, result)
	}
	var started atomic.Int32
	h.gateway.options.Start = func(_ context.Context, info SessionInfo, _ string) (string, error) {
		if info.StartedBy == nil || info.StartedBy.Kind != "mcp" || info.StartedBy.User != testBrowserEmail {
			t.Errorf("MCP identity lost: %+v", info.StartedBy)
		}
		started.Add(1)
		return "session-test", nil
	}
	directory := filepath.Join(h.gateway.options.Home, "project")
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "agent": "codex", "terminal": true}})
	if err != nil || !result.IsError || started.Load() != 0 {
		t.Fatalf("unknown folder accepted: %v %+v", err, result)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(h.gateway.options.Home, SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "start_session", Arguments: map[string]any{"directory": directory, "agent": "codex", "terminal": true}})
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
	if err := upsertSession(t.Context(), h.gateway.auth.store.DB, SessionInfo{ID: record.ID}); err != nil {
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
	err := state.Transaction(context.Background(), h.gateway.store.DB, func(tx *sql.Tx) error {
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

func TestOAuthCodeConsumedOnceAcrossStores(t *testing.T) {
	h := newOAuthHarness(t)
	form := h.codeForm(h.code(t)).Encode()
	other, err := state.Open(h.gateway.options.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second := &GatewayMCP{gateway: &Gateway{store: other, origin: h.gateway.origin}}
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, m := range []*GatewayMCP{h.gateway.mcp, second} {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, h.server.URL+"/oauth/token", strings.NewReader(form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			m.token(w, r)
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
		"/big.json":          `{"client_id":"https://client.example.com/big.json","redirect_uris":["https://assistant.example/callback"],"pad":"` + strings.Repeat("x", clientMetadataLimit) + `"}`,
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
	h.gateway.mcp.fetcher = client
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
	for _, address := range []string{"127.0.0.1:443", "10.1.2.3:443", "100.101.102.103:443", "[fd7a:115c:a1e0::1]:443", "169.254.169.254:80", "[::ffff:127.0.0.1]:443", "[64:ff9b::a00:1]:443", "[2002:a00:1::1]:443"} {
		if publicAddressOnly("tcp", address, nil) == nil {
			t.Fatalf("metadata fetch may reach %s", address)
		}
	}
	if err := publicAddressOnly("tcp", "93.184.215.14:443", nil); err != nil {
		t.Fatal(err)
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
	limited := func(address string) bool {
		r := httptest.NewRequest("POST", "/oauth/token", nil)
		r.RemoteAddr = address
		return h.gateway.mcp.limited(httptest.NewRecorder(), r)
	}
	for range 120 {
		limited("203.0.113.1:1")
	}
	if !limited("203.0.113.1:2") || limited("203.0.113.2:1") {
		t.Fatal("OAuth limit is not per client address")
	}
}
