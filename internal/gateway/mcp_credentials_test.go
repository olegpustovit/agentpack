package gateway

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

// fakeMCPAuthServer is an MCP server behind an OAuth server that rotates refresh tokens and rejects reuse.
type fakeMCPAuthServer struct {
	*httptest.Server
	mu        sync.Mutex
	access    string
	refresh   string
	serial    int
	refreshes atomic.Int32
	onRefresh func() // runs before the token endpoint answers a refresh
}

func newFakeMCPAuthServer(t *testing.T) *fakeMCPAuthServer {
	fake := &fakeMCPAuthServer{}
	mux := http.NewServeMux()
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		network.WriteJSON(w, map[string]any{"resource": fake.URL + "/mcp", "authorization_servers": []string{fake.URL}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		network.WriteJSON(w, map[string]any{"issuer": fake.URL, "authorization_endpoint": fake.URL + "/authorize", "token_endpoint": fake.URL + "/token",
			"registration_endpoint": fake.URL + "/register", "revocation_endpoint": fake.URL + "/revoke", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var metadata map[string]any
		_ = json.NewDecoder(r.Body).Decode(&metadata)
		metadata["client_id"] = "dynamic-client"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(metadata)
	})
	// The browser's consent: send the code straight back to the gateway.
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		http.Redirect(w, r, query.Get("redirect_uri")+"?code=granted&state="+url.QueryEscape(query.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "granted" || r.Form.Get("code_verifier") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
		case "refresh_token":
			if r.Form.Get("refresh_token") != fake.refresh {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			fake.refreshes.Add(1)
			if fake.onRefresh != nil {
				fake.onRefresh()
			}
		}
		fake.serial++
		fake.access, fake.refresh = fmt.Sprintf("at-%d", fake.serial), fmt.Sprintf("rt-%d", fake.serial)
		network.WriteJSON(w, map[string]any{"access_token": fake.access, "refresh_token": fake.refresh, "token_type": "bearer", "expires_in": 3600})
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		valid := fake.access != "" && r.Header.Get("Authorization") == "Bearer "+fake.access
		fake.mu.Unlock()
		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+fake.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&message)
		if len(message.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{"tools": []any{}, "toolsets": r.Header.Get("X-Toolsets")}
		if message.Method == "initialize" {
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}
			w.Header().Set("Mcp-Session-Id", "session-1")
		} else if r.Header.Get("MCP-Protocol-Version") != "2025-06-18" || r.Header.Get("Mcp-Session-Id") != "session-1" {
			http.Error(w, "missing session headers", http.StatusBadRequest)
			return
		}
		network.WriteJSON(w, map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
	})
	return fake
}

// expire makes the server reject the current access token, as after its expiry.
func (fake *fakeMCPAuthServer) expire() {
	fake.mu.Lock()
	fake.access = "expired"
	fake.mu.Unlock()
}

func startCredentialGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth, local: network.NodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.Handler().ServeHTTP(w, r) })
	server.Start()
	t.Cleanup(server.Close)
	g.origin, _ = url.Parse(server.URL)
	g.local.Address = server.Listener.Addr().String()
	data, _ := json.Marshal(g.local)
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return g, home
}

func localCall(t *testing.T, home, method, path string, input, output any) {
	t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	data, err := NodeRequest(context.Background(), home, method, path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			t.Fatal(err)
		}
	}
}

// runTestBridge drives one bridge: initialize, initialized, then a tools/list per call of next.
func runTestBridge(t *testing.T, home, id, endpoint string) (next func() map[string]any, stop func()) {
	t.Helper()
	stdinReader, stdin := io.Pipe()
	stdoutReader, stdout := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunMCPBridge(context.Background(), MCPBridgeOptions{Home: home, ID: id, URL: endpoint, Headers: map[string]string{"X-Toolsets": "repos"}, Stdin: stdinReader, Stdout: stdout, Stderr: io.Discard})
		stdout.Close()
	}()
	lines := bufio.NewScanner(stdoutReader)
	read := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			t.Fatal("bridge closed its output")
		}
		var message map[string]any
		if err := json.Unmarshal(lines.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		if message["error"] != nil {
			t.Fatalf("bridge error: %v", message["error"])
		}
		return message
	}
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	read()
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	request := 1
	return func() map[string]any {
			request++
			fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`+"\n", request)
			return read()["result"].(map[string]any)
		}, func() {
			stdin.Close()
			<-done
		}
}

func TestMCPCredentialOAuthLoginBridgeAndSingleRefresh(t *testing.T) {
	fake := newFakeMCPAuthServer(t)
	g, home := startCredentialGateway(t)
	var started struct {
		Login        string `json:"login"`
		AuthorizeURL string `json:"authorize_url"`
	}
	localCall(t, home, http.MethodPost, "/api/mcp/logins", map[string]string{"url": fake.URL + "/mcp", "label": "Fake"}, &started)
	browser, err := http.Get(started.AuthorizeURL) // consent, gateway callback, then the Vault page
	if err != nil {
		t.Fatal(err)
	}
	browser.Body.Close()
	if browser.Request.URL.Path != "/settings/connections" {
		t.Fatalf("browser ended at %s", browser.Request.URL)
	}
	if got := browser.Request.URL.Query().Get("oauth_login"); got != started.Login {
		t.Fatal("Vault could not retrieve the login outcome")
	}
	var status map[string]string
	localCall(t, home, http.MethodGet, "/api/mcp/logins/"+started.Login, nil, &status)
	if status["status"] != "connected" {
		t.Fatalf("login status %v", status)
	}
	var list struct {
		Credentials []MCPCredentialInfo `json:"credentials"`
	}
	localCall(t, home, http.MethodGet, "/api/mcp/credentials", nil, &list)
	if len(list.Credentials) != 1 || list.Credentials[0].Match != fake.URL+"/mcp" || list.Credentials[0].Status != mcpStatusConnected || list.Credentials[0].Account != testBrowserEmail {
		t.Fatalf("credentials %+v", list.Credentials)
	}
	id := list.Credentials[0].ID

	if err := g.writeMCPIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	index, err := mcp.ReadCredentialIndex(filepath.Join(home, "mcp-credentials.json"))
	if err != nil || len(index.Credentials) != 1 || index.Credentials[0].ID != id {
		t.Fatalf("index %+v %v", index, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "mcp-credentials.json"))
	if bytes.Contains(raw, []byte("at-1")) || bytes.Contains(raw, []byte("rt-1")) {
		t.Fatal("the staging index holds a token")
	}

	first, stopFirst := runTestBridge(t, home, id, fake.URL+"/mcp")
	defer stopFirst()
	second, stopSecond := runTestBridge(t, home, id, fake.URL+"/mcp")
	defer stopSecond()
	if result := first(); result["toolsets"] != "repos" {
		t.Fatalf("server did not receive the configured header: %v", result)
	}
	// Both bridges hold the same token when it expires: exactly one refresh may reach the server.
	fake.expire()
	var wait sync.WaitGroup
	for _, next := range []func() map[string]any{first, second} {
		wait.Add(1)
		go func() { defer wait.Done(); next() }()
	}
	wait.Wait()
	if count := fake.refreshes.Load(); count != 1 {
		t.Fatalf("refreshes = %d, want 1", count)
	}
	credential, err := g.loadMCPCredential(context.Background(), id)
	if err != nil || credential.State.Generation != 2 || credential.State.Token.RefreshToken != "rt-2" {
		t.Fatalf("stored state %+v %v", credential.State, err)
	}
}

func TestMCPCredentialSecretStaysLocal(t *testing.T) {
	g, home := startCredentialGateway(t)
	var created struct {
		ID string `json:"id"`
	}
	localCall(t, home, http.MethodPost, "/api/mcp/credentials", map[string]any{"kind": "header", "url": "https://pat.example.com/mcp", "values": map[string]string{"Authorization": "Bearer ghp_secret"}}, &created)
	var list struct {
		Credentials []MCPCredentialInfo `json:"credentials"`
	}
	localCall(t, home, http.MethodGet, "/api/mcp/credentials", nil, &list)
	listed, _ := json.Marshal(list)
	if bytes.Contains(listed, []byte("ghp_secret")) || len(list.Credentials) != 1 || list.Credentials[0].Label != "pat.example.com" {
		t.Fatalf("list %s", listed)
	}
	// A peer acting for the same account may not read the secret.
	request := httptest.NewRequest(http.MethodGet, g.origin.String()+"/api/mcp/credentials/"+created.ID+"/secret", nil)
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	request = request.WithContext(session.WithActor(context.WithValue(request.Context(), replicaPeerKey{}, "peer"), session.Actor{Kind: "peer", User: testBrowserEmail, Account: true, StableNodeID: "peer"}))
	response := httptest.NewRecorder()
	g.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "ghp_secret") {
		t.Fatalf("peer secret read: %d %s", response.Code, response.Body)
	}
	var secret mcpBridgeSecret
	localCall(t, home, http.MethodGet, "/api/mcp/credentials/"+created.ID+"/secret", nil, &secret)
	if secret.Headers["Authorization"] != "Bearer ghp_secret" {
		t.Fatalf("local secret %+v", secret)
	}
	// Saving the same URL again replaces it in place, keeping the ID staging refers to.
	localCall(t, home, http.MethodPost, "/api/mcp/credentials", map[string]any{"kind": "header", "url": "https://PAT.example.com/mcp/", "values": map[string]string{"Authorization": "Bearer ghp_new"}}, &created)
	localCall(t, home, http.MethodGet, "/api/mcp/credentials", nil, &list)
	if len(list.Credentials) != 1 || list.Credentials[0].ID != created.ID {
		t.Fatalf("replace created a second row: %+v", list.Credentials)
	}
}

// replicateCredential copies a credential row the way state sync would.
func replicateCredential(t *testing.T, from, to *Gateway, id string) {
	t.Helper()
	credential, err := from.loadMCPCredential(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := json.Marshal(credential.Client)
	state, _ := json.Marshal(credential.State)
	if _, err := to.store.DB.Exec("INSERT OR REPLACE INTO mcp_credentials(id,match,kind,label,account,client,state,created_node,created_at) VALUES (?,?,?,?,?,?,?,?,?)",
		credential.ID, credential.Match, credential.Kind, credential.Label, credential.Account, string(client), string(state), credential.CreatedNode, credential.CreatedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestMCPCredentialPeerRefreshesThroughRefresher(t *testing.T) {
	fake := newFakeMCPAuthServer(t)
	main, mainHome := startCredentialGateway(t)
	peer, peerHome := startCredentialGateway(t)
	mainID, err := main.store.NodeID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	main.options.Library.Store, peer.options.Library.Store = replicaStore(t), replicaStore(t)
	if err := main.options.Library.Store.EnrollPeer("peer-node"); err != nil {
		t.Fatal(err)
	}
	if err := peer.options.Library.Store.EnrollPeer(mainID); err != nil {
		t.Fatal(err)
	}
	// Main's tailnet listener: what ServePeer attaches to a verified mesh request.
	mainMesh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := session.Actor{Kind: "peer", User: r.Header.Get("X-Agentpack-User"), Account: true, StableNodeID: "peer-node"}
		r = r.Clone(session.WithActor(context.WithValue(r.Context(), replicaPeerKey{}, "peer-node"), actor))
		r.Header.Set("Authorization", "Bearer "+main.local.Secret)
		r.Host = main.origin.Host
		main.Handler().ServeHTTP(w, r)
	}))
	defer mainMesh.Close()
	reachable := true
	peer.mcpBroker.target = func(_ context.Context, node string) (*url.URL, error) {
		if node != mainID || !reachable {
			return nil, fmt.Errorf("node %s offline", node)
		}
		return url.Parse(mainMesh.URL)
	}

	var started struct {
		Login        string `json:"login"`
		AuthorizeURL string `json:"authorize_url"`
	}
	localCall(t, mainHome, http.MethodPost, "/api/mcp/logins", map[string]string{"url": fake.URL + "/mcp"}, &started)
	browser, err := http.Get(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	browser.Body.Close()
	var list struct {
		Credentials []MCPCredentialInfo `json:"credentials"`
	}
	localCall(t, mainHome, http.MethodGet, "/api/mcp/credentials", nil, &list)
	if len(list.Credentials) != 1 {
		t.Fatalf("credentials %+v", list.Credentials)
	}
	id := list.Credentials[0].ID
	replicateCredential(t, main, peer, id)

	next, stop := runTestBridge(t, peerHome, id, fake.URL+"/mcp")
	defer stop()
	next()
	fake.expire()
	next()
	if count := fake.refreshes.Load(); count != 1 {
		t.Fatalf("refreshes = %d, want 1 (by Main)", count)
	}
	onMain, _ := main.loadMCPCredential(context.Background(), id)
	onPeer, _ := peer.loadMCPCredential(context.Background(), id)
	if onMain.State.Generation != 2 || onPeer.State.Generation != 1 {
		t.Fatalf("generations main=%d peer=%d: only the refresher writes", onMain.State.Generation, onPeer.State.Generation)
	}

	fresh, err := main.markNeedsLogin(t.Context(), onPeer)
	if err != nil || fresh.State.Generation != onMain.State.Generation || fresh.State.Status != mcpStatusConnected {
		t.Fatalf("stale reconnect overwrote the newer token: generation=%d status=%q err=%v", fresh.State.Generation, fresh.State.Status, err)
	}

	// An offline Main leaves the peer's replicated credential intact.
	reachable = false
	fake.expire()
	if _, err := peer.freshMCPSecret(t.Context(), id, 2); !errors.Is(err, errMCPRefresherUnreachable) {
		t.Fatalf("refresh with Main offline: %v", err)
	}
	if count := fake.refreshes.Load(); count != 1 {
		t.Fatalf("refreshes = %d after Main went offline", count)
	}
	onPeer, err = peer.loadMCPCredential(t.Context(), id)
	if err != nil || onPeer.State.Status != mcpStatusConnected {
		t.Fatalf("peer status=%q err=%v", onPeer.State.Status, err)
	}
}

func TestMCPCredentialRevokeAllNeedsReconnect(t *testing.T) {
	g, home := startCredentialGateway(t)
	token := &mcpToken{AccessToken: "at", RefreshToken: "rt", IssuedAt: time.Now()}
	id, err := g.saveMCPCredential(context.Background(), mcpCredential{Kind: mcp.CredentialOAuth, Match: "https://x.example/mcp", Label: "X", Account: testBrowserEmail,
		State: mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Token: token}})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]int
	localCall(t, home, http.MethodPost, "/api/mcp/credentials/revoke-all", nil, &result)
	credential, _ := g.loadMCPCredential(context.Background(), id)
	if result["revoked"] != 1 || credential.State.Status != mcpStatusNeedsLogin || credential.State.Token != nil || credential.State.Generation != 2 {
		t.Fatalf("revoke-all %v → %+v", result, credential.State)
	}
}

// An SSE bridge connects when the first message arrives; one started while its credential needs login
// must serve calls once the credential is valid again, without a restart.
func TestMCPBridgeReconnectsAfterLogin(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "ping"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
	})
	sse := sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sse.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	var valid atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !valid.Load() {
			network.WriteJSON(w, mcpBridgeSecret{Label: "Fake", Status: mcpStatusNeedsLogin})
			return
		}
		network.WriteJSON(w, mcpBridgeSecret{Status: mcpStatusConnected, Headers: map[string]string{"Authorization": "Bearer good"}, Generation: 1})
	}))
	t.Cleanup(gateway.Close)
	home := t.TempDir()
	data, _ := json.Marshal(network.NodeRecord{Address: gateway.Listener.Addr().String(), Secret: strings.Repeat("x", 64)})
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	stdinReader, stdin := io.Pipe()
	stdoutReader, stdout := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunMCPBridge(context.Background(), MCPBridgeOptions{Home: home, ID: "fake", URL: upstream.URL, SSE: true, Stdin: stdinReader, Stdout: stdout, Stderr: io.Discard})
		stdout.Close()
	}()
	defer func() { stdin.Close(); <-done }()
	lines := bufio.NewScanner(stdoutReader)
	call := func(id int, method, params string) map[string]any {
		t.Helper()
		fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`+"\n", id, method, params)
		if !lines.Scan() {
			t.Fatal("bridge closed its output")
		}
		var message map[string]any
		if err := json.Unmarshal(lines.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	initialize := `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`
	if message := call(1, "initialize", initialize); !strings.Contains(fmt.Sprint(message["error"]), "needs login") {
		t.Fatalf("initialize before login: %v", message)
	}
	valid.Store(true)
	if message := call(2, "initialize", initialize); message["error"] != nil {
		t.Fatalf("initialize after login: %v", message)
	}
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if message := call(3, "tools/call", `{"name":"ping","arguments":{}}`); !strings.Contains(fmt.Sprint(message["result"]), "pong") {
		t.Fatalf("tool call after login: %v", message)
	}
}

// A refresh that finishes after a new login was saved must not put the old grant's token back.
func TestMCPRefreshLosesToNewerLogin(t *testing.T) {
	g, _ := startCredentialGateway(t)
	ctx := context.Background()
	login := func(generation int64, access string) string {
		t.Helper()
		id, err := g.saveMCPCredential(ctx, mcpCredential{Kind: mcp.CredentialOAuth, Match: "https://x.example/mcp", Label: "X", Account: testBrowserEmail,
			State: mcpCredentialState{Generation: generation, Status: mcpStatusConnected, Token: &mcpToken{AccessToken: access, RefreshToken: "rt", IssuedAt: time.Now()}}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := login(1, "old")
	loaded, err := g.loadMCPCredential(ctx, id) // the refresh starts from this
	if err != nil {
		t.Fatal(err)
	}
	login(1, "new") // the user logs in again meanwhile; SaveMCP raises the generation
	stale := loaded.State
	stale.Generation++
	stale.Token = &mcpToken{AccessToken: "refreshed-old", RefreshToken: "rt", IssuedAt: time.Now()}
	if err := g.writeMCPState(ctx, id, stale); err != nil {
		t.Fatal(err)
	}
	if current, _ := g.loadMCPCredential(ctx, id); current.State.Token == nil || current.State.Token.AccessToken != "new" {
		t.Fatalf("stale refresh overwrote the new login: %+v", current.State.Token)
	}
}

// The server rotates the token as soon as it is asked: a caller that goes away (a shutdown) must not lose the answer.
func TestMCPCredentialRefreshSurvivesCancel(t *testing.T) {
	fake := newFakeMCPAuthServer(t)
	fake.refresh = "rt"
	g, _ := startCredentialGateway(t)
	id, err := g.saveMCPCredential(t.Context(), mcpCredential{Kind: mcp.CredentialOAuth, Match: fake.URL + "/mcp", Label: "X", Account: testBrowserEmail,
		Client: mcpOAuthClient{ClientID: "dynamic-client", TokenURL: fake.URL + "/token"},
		State:  mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Token: &mcpToken{AccessToken: "old", RefreshToken: "rt", IssuedAt: time.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	fake.onRefresh = cancel
	if _, err := g.refreshMCPToken(ctx, id, 1); err != nil {
		t.Fatal(err)
	}
	current, err := g.loadMCPCredential(t.Context(), id)
	if err != nil || current.State.Generation != 2 || current.State.Token.RefreshToken != "rt-1" {
		t.Fatalf("rotated token lost: generation %d, %v", current.State.Generation, err)
	}
}

// setEnvCredential saves an env credential directly, as a replicated row would arrive, and gives it to agents.
func setEnvCredential(t *testing.T, g *Gateway, account, label string, values map[string]string, toAgents bool) string {
	t.Helper()
	id, err := g.saveMCPCredential(t.Context(), mcpCredential{Kind: mcp.CredentialEnv, Label: label, Account: account,
		State: mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Env: values}})
	if err != nil {
		t.Fatal(err)
	}
	if toAgents {
		if err := g.store.Credentials().Approve(t.Context(), id, mcp.AgentsModule); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestMCPCredentialAgentsApprovalAndReservedNames(t *testing.T) {
	g, home := startCredentialGateway(t)
	post := func(path string, input any) error {
		body, _ := json.Marshal(input)
		_, err := NodeRequest(t.Context(), home, http.MethodPost, path, bytes.NewReader(body))
		return err
	}
	save := func(label, name string) (string, error) {
		body, _ := json.Marshal(map[string]any{"kind": "env", "label": label, "values": map[string]string{name: "x"}})
		data, err := NodeRequest(t.Context(), home, http.MethodPost, "/api/mcp/credentials", bytes.NewReader(body))
		var created struct {
			ID string `json:"id"`
		}
		if err == nil {
			err = json.Unmarshal(data, &created)
		}
		return created.ID, err
	}
	approve := func(id string) error {
		return post("/api/mcp/credentials/"+id+"/approvals", map[string]string{"module": mcp.AgentsModule})
	}
	given, err := save("given", "PATHFINDER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if err := approve(given); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"PATH", "Path", "HOME", "SHELL", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "AGENTPACK_HOME", "agentpack_trace"} {
		// A local MCP server may need such a name; only agents never get it.
		label := fmt.Sprintf("server-%d", i)
		id, err := save(label, name)
		if err != nil {
			t.Fatalf("%s not saved: %v", name, err)
		}
		if _, err := save(label, name); err != nil {
			t.Fatalf("%s not replaced: %v", name, err)
		}
		if err := approve(id); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("%s given to agents: %v", name, err)
		}
		if _, err := save("given", name); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("%s saved into a credential agents get: %v", name, err)
		}
	}
	var header struct {
		ID string `json:"id"`
	}
	localCall(t, home, http.MethodPost, "/api/mcp/credentials", map[string]any{"kind": "header", "url": "https://x.example/mcp", "values": map[string]string{"Authorization": "Bearer v"}}, &header)
	if err := approve(header.ID); err == nil {
		t.Fatal("a header credential was given to agents")
	}
	// A row from another machine may hold a name this one refuses.
	if err := approve(setEnvCredential(t, g, testBrowserEmail, "replicated", map[string]string{"LD_PRELOAD": "x"}, false)); err == nil {
		t.Fatal("a reserved variable was given to agents")
	}
}

// Each way of starting a session records its actor in StartedBy; that actor's account alone decides.
func TestAgentEnvFollowsTheSessionsAccount(t *testing.T) {
	g, home := startCredentialGateway(t)
	const other = "other@example.com"
	testBrowserSession(t, g.auth, other, "other-cookie")
	mine := setEnvCredential(t, g, testBrowserEmail, "mine", map[string]string{"VAULT_TOKEN": "s3cret-value", "PATH": "/evil", "ld_preload": "x", "NUL": "a\x00b"}, true)
	setEnvCredential(t, g, testBrowserEmail, "bridge-only", map[string]string{"BRIDGE_TOKEN": "bridge-value"}, false)
	// The machine's local binding is another account; it must not matter.
	if _, err := g.store.DB.Exec("UPDATE local_node SET account_email=? WHERE id=1", other); err != nil {
		t.Fatal(err)
	}
	browser := session.BrowserActor(testBrowserEmail)
	agent := browser
	agent.Agent = "claude"
	sessions := 0
	read := func(startedBy *session.Actor, transport string) network.AgentSecrets {
		t.Helper()
		sessions++
		id := fmt.Sprintf("session-env-%d", sessions)
		if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: id, Harness: "claude", Transport: transport, StartedBy: startedBy}); err != nil {
			t.Fatal(err)
		}
		var secrets network.AgentSecrets
		localCall(t, home, http.MethodGet, "/api/sessions/"+id+"/agent-env", nil, &secrets)
		return secrets
	}
	for name, actor := range map[string]session.Actor{
		"browser": browser,
		"agent":   agent,
		"mcp":     {Kind: "mcp", User: testBrowserEmail},
		"peer":    {Kind: "peer", User: testBrowserEmail, Account: true},
		"local":   {Kind: "local", User: testBrowserEmail, Account: true},
	} {
		for _, transport := range []string{"acp", "terminal"} {
			got := read(&actor, transport)
			if len(got.Env) != 1 || got.Env["VAULT_TOKEN"] != "s3cret-value" || len(got.Credentials) != 1 || got.Credentials[0] != mine {
				t.Fatalf("%s %s: %+v", name, transport, got)
			}
		}
	}
	nothing := func(why string, startedBy *session.Actor, transport string) {
		t.Helper()
		if got := read(startedBy, transport); len(got.Env) != 0 || len(got.Credentials) != 0 {
			t.Fatalf("%s got %+v", why, got)
		}
	}
	second := session.BrowserActor(other)
	nothing("a second account", &second, "acp")
	nothing("a shell", &browser, "shell")
	nothing("an unverified peer user", &session.Actor{Kind: "peer", User: testBrowserEmail}, "acp")
	nothing("an OS user", &session.Actor{Kind: "cli", User: testBrowserEmail}, "terminal")
	nothing("no actor", nil, "acp")
	if _, err := g.store.DB.Exec("UPDATE users SET disabled=1 WHERE email=?", testBrowserEmail); err != nil {
		t.Fatal(err)
	}
	nothing("a disabled account", &browser, "acp")
}

func TestAgentEnvAndSecretRefuseEveryoneButTheCLI(t *testing.T) {
	g, home := startCredentialGateway(t)
	id := setEnvCredential(t, g, testBrowserEmail, "mine", map[string]string{"VAULT_TOKEN": "s3cret-value"}, true)
	owner := session.BrowserActor(testBrowserEmail)
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: "session-env", Transport: "acp", StartedBy: &owner}); err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, g.auth, testBrowserEmail, "cookie")
	for _, path := range []string{"/api/sessions/session-env/agent-env", "/api/mcp/credentials/" + id + "/secret"} {
		var secrets any
		localCall(t, home, http.MethodGet, path, nil, &secrets) // the CLI may
		request := func(actor session.Actor) *http.Request {
			r := httptest.NewRequest(http.MethodGet, g.origin.String()+path, nil)
			return r.WithContext(session.WithActor(r.Context(), actor))
		}
		browser := httptest.NewRequest(http.MethodGet, g.origin.String()+path, nil)
		browser.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "cookie"})
		peer := request(session.Actor{Kind: "peer", User: testBrowserEmail, Account: true, StableNodeID: "peer"})
		peer.Header.Set("Authorization", "Bearer "+g.local.Secret)
		peer = peer.WithContext(withCaller(peer.Context(), callerPeer))
		refused := map[string]func(http.ResponseWriter){
			"browser": func(w http.ResponseWriter) { g.Handler().ServeHTTP(w, browser) },
			"peer":    func(w http.ResponseWriter) { g.Handler().ServeHTTP(w, peer) },
			"mcp": func(w http.ResponseWriter) {
				g.ServeAPI(w, request(session.Actor{Kind: "mcp", User: testBrowserEmail}), "")
			},
			// A chat started on this machine's CLI route acts as a local actor through its tools.
			"chat agent": func(w http.ResponseWriter) {
				g.ServeAPI(w, request(session.Actor{Kind: "local", User: testBrowserEmail, Account: true, Agent: "claude", Chat: "session-env"}), "")
			},
		}
		for caller, serve := range refused {
			w := httptest.NewRecorder()
			serve(w)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "s3cret-value") {
				t.Fatalf("%s %s: %d %s", caller, path, w.Code, w.Body)
			}
		}
	}
}

// A real terminal worker: the agent has the value, a shell does not, and agentpack stores it nowhere.
func TestVaultSecretsReachOnlyTheAgentProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	g, home := startCredentialGateway(t)
	credential := setEnvCredential(t, g, testBrowserEmail, "mine", map[string]string{"VAULT_TOKEN": "s3cret-first"}, true)
	for _, transport := range []string{"", "shell"} {
		t.Run("transport="+transport, func(t *testing.T) {
			out := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			ready, stopped := make(chan string, 1), make(chan struct{})
			owner := session.BrowserActor(testBrowserEmail)
			go func() {
				defer close(stopped)
				_, err := network.RunWorker(ctx, home, "test", network.WorkerLaunch{
					Transport: transport, Program: "/bin/sh",
					// Each start writes what it saw to a new numbered file.
					Arguments: []string{"-c", `n=1; while [ -e "$OUT/$n" ]; do n=$((n+1)); done; printf %s "${VAULT_TOKEN-unset}" >"$OUT/tmp" && mv "$OUT/tmp" "$OUT/$n"; exec sleep 30`},
					Directory: out, Environment: []string{"PATH=" + os.Getenv("PATH"), "OUT=" + out}, Rows: 24, Columns: 80,
					Session: session.SessionInfo{Harness: "claude", Transport: transport, Directory: out, StartedBy: &owner},
				}, func(id string) error { ready <- id; return nil })
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			}()
			defer func() { cancel(); <-stopped }()
			var id string
			select {
			case id = <-ready:
			case <-stopped:
				t.Fatal("worker did not start")
			}
			seen := func(name string) string {
				t.Helper()
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					if data, err := os.ReadFile(filepath.Join(out, name)); err == nil {
						return string(data)
					}
				}
				t.Fatal("the process never wrote " + name)
				return ""
			}
			want := "s3cret-first"
			if transport == "shell" {
				want = "unset"
			}
			if got := seen("1"); got != want {
				t.Fatalf("first start saw %q, want %q", got, want)
			}
			if transport == "shell" {
				return
			}
			// A replaced value applies at the next start.
			setEnvCredential(t, g, testBrowserEmail, "mine", map[string]string{"VAULT_TOKEN": "s3cret-second"}, true)
			record, err := network.ReadWorkerRecord(home, id)
			if err != nil {
				t.Fatal(err)
			}
			response, err := network.WorkerRequest(ctx, record, http.MethodPost, "/restart", "vault-restart-operation")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if got := seen("2"); got != "s3cret-second" {
				t.Fatalf("restart saw %q", got)
			}
			cancel()
			<-stopped
			rows, err := g.store.Reader.Query("SELECT * FROM sessions")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for rows.Next() {
				values := make([]any, len(columns))
				for i := range values {
					values[i] = new(sql.RawBytes)
				}
				if err := rows.Scan(values...); err != nil {
					t.Fatal(err)
				}
				for i, value := range values {
					if bytes.Contains(*value.(*sql.RawBytes), []byte("s3cret")) {
						t.Fatal("a session row holds a value in " + columns[i])
					}
				}
				count++
			}
			if count == 0 || rows.Err() != nil {
				t.Fatal("no session row to check", rows.Err())
			}
			// Everything agentpack wrote, apart from the database that stores the credential itself.
			recorded := false
			err = filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), "state.db") {
					return err
				}
				data, err := os.ReadFile(path)
				if bytes.Contains(data, []byte("s3cret")) {
					t.Error("a value was written to " + path)
				}
				recorded = recorded || bytes.Contains(data, []byte(`"vault"`)) && bytes.Contains(data, []byte(credential))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !recorded {
				t.Error("no vault.use event with the connection ID was recorded")
			}
		})
	}
}

func TestListSecretsNamesOnly(t *testing.T) {
	h := newOAuthHarness(t)
	g := h.gateway
	id := setEnvCredential(t, g, testBrowserEmail, "gh", map[string]string{"VAULT_TOKEN": "s3cret-env"}, true)
	if _, err := g.saveMCPCredential(t.Context(), mcpCredential{Kind: mcp.CredentialHeader, Match: "https://x.example/mcp", Label: "X", Account: testBrowserEmail,
		State: mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Headers: map[string]string{"Authorization": "Bearer s3cret-header"}}}); err != nil {
		t.Fatal(err)
	}
	const other = "other@example.com"
	testBrowserSession(t, g.auth, other, "other-cookie")
	mine := chatFixture(t, h, "session-mine", "")
	theirs := chatFixture(t, h, "session-theirs", "")
	member := session.BrowserActor(other)
	if err := session.UpsertSession(t.Context(), g.store.DB, session.SessionInfo{ID: "session-theirs", Harness: "claude", Transport: "acp", Tools: true, StartedBy: &member}); err != nil {
		t.Fatal(err)
	}
	list := func(token string) (listed struct {
		Agents      []map[string]any `json:"agents"`
		Connections []map[string]any `json:"connections"`
	}, text string) {
		t.Helper()
		result, text := callTool(t, mcpClient(t, h, token), "list_secrets", nil)
		if result.IsError {
			t.Fatal(text)
		}
		data, _ := json.Marshal(result)
		if bytes.Contains(data, []byte("s3cret")) {
			t.Fatalf("a value left the gateway: %s", data)
		}
		if err := json.Unmarshal([]byte(text), &listed); err != nil {
			t.Fatal(err)
		}
		return listed, text
	}
	// The owner's chat and the owner's own MCP client see the same names.
	for _, token := range []string{mine, h.exchange(t, h.codeForm(h.code(t)), 200).Access} {
		got, text := list(token)
		if len(got.Connections) != 2 || len(got.Agents) == 0 || !strings.Contains(text, "VAULT_TOKEN") ||
			got.Connections[0]["id"] != id || got.Connections[0]["given_to_agents"] != true || got.Connections[1]["given_to_agents"] != false {
			t.Fatalf("list_secrets: %s", text)
		}
	}
	if got, text := list(theirs); len(got.Connections) != 0 || len(got.Agents) != 0 {
		t.Fatalf("another account saw: %s", text)
	}
	// The one route MCP gained is the list: the secret route still refuses it.
	r := httptest.NewRequest(http.MethodGet, "/api/mcp/credentials/"+id+"/secret", nil)
	w := httptest.NewRecorder()
	g.ServeAPI(w, r.WithContext(session.WithActor(r.Context(), session.Actor{Kind: "mcp", User: testBrowserEmail})), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("secret route: %d %s", w.Code, w.Body)
	}
}
