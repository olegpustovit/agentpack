package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

// fakeMCPAuthServer is an MCP server behind an OAuth server that rotates refresh tokens and rejects reuse.
type fakeMCPAuthServer struct {
	*httptest.Server
	mu        sync.Mutex
	access    string
	refresh   string
	serial    int
	refreshes atomic.Int32
}

func newFakeMCPAuthServer(t *testing.T) *fakeMCPAuthServer {
	fake := &fakeMCPAuthServer{}
	mux := http.NewServeMux()
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": fake.URL + "/mcp", "authorization_servers": []string{fake.URL}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": fake.URL, "authorization_endpoint": fake.URL + "/authorize", "token_endpoint": fake.URL + "/token",
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
		}
		fake.serial++
		fake.access, fake.refresh = fmt.Sprintf("at-%d", fake.serial), fmt.Sprintf("rt-%d", fake.serial)
		writeJSON(w, map[string]any{"access_token": fake.access, "refresh_token": fake.refresh, "token_type": "bearer", "expires_in": 3600})
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
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
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
	g := &Gateway{store: auth.store, auth: auth, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
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
	browser, err := http.Get(started.AuthorizeURL) // consent, gateway callback, then the Connections page
	if err != nil {
		t.Fatal(err)
	}
	browser.Body.Close()
	if browser.Request.URL.Path != "/settings/connections" {
		t.Fatalf("browser ended at %s", browser.Request.URL)
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
	request = request.WithContext(withActor(context.WithValue(request.Context(), replicaPeerKey{}, "peer"), Actor{Kind: "peer", User: testBrowserEmail, Account: true, StableNodeID: "peer"}))
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
	// Main's tailnet listener: what peerHandler attaches to a verified mesh request.
	mainMesh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := Actor{Kind: "peer", User: r.Header.Get("X-Agentpack-User"), Account: true, StableNodeID: "peer-node"}
		r = r.Clone(withActor(context.WithValue(r.Context(), replicaPeerKey{}, "peer-node"), actor))
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
