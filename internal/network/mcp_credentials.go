package network

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/oauth2"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// MCP credentials are stored once in the replicated mcp_credentials table and
// reach agents through `agentpack mcp-bridge`, so a login works on every
// paired node, harness, project and mode. Mutable state (token set, status,
// generation) lives in one JSON cell so last-writer-wins replication can never
// mix fields from two refreshes. One node, Main (or the creating node without
// a Main), refreshes each OAuth credential; others ask it over the mesh.

const (
	mcpStatusConnected  = "connected"
	mcpStatusNeedsLogin = "needs_login"
	// mcpRefreshMargin refreshes a token this long before it expires.
	mcpRefreshMargin = time.Minute
)

var (
	errMCPNeedsLogin           = errors.New("MCP credential needs login")
	errMCPCredentialNotFound   = errors.New("MCP credential not found")
	errMCPRefresherUnreachable = errors.New("MCP credential refresher is unreachable")
	headerNamePattern          = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)
	envNamePattern             = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type mcpToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

type mcpCredentialState struct {
	Generation int64             `json:"generation"`
	Status     string            `json:"status"`
	Headers    map[string]string `json:"headers,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Token      *mcpToken         `json:"token,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at"`
	UpdatedBy  string            `json:"updated_by,omitempty"`
}

type mcpOAuthClient struct {
	ClientID      string   `json:"client_id"`
	ClientSecret  string   `json:"client_secret,omitempty"`
	AuthURL       string   `json:"auth_url"`
	TokenURL      string   `json:"token_url"`
	AuthStyle     int      `json:"auth_style,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	RedirectURL   string   `json:"redirect_url,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	RevocationURL string   `json:"revocation_url,omitempty"`
}

type mcpCredential struct {
	ID, Match, Kind, Label, Account, CreatedNode string
	CreatedAt                                    time.Time
	Client                                       mcpOAuthClient
	State                                        mcpCredentialState
	Plugins                                      []string
}

// MCPCredentialInfo is what lists show: names of headers and variables, never values.
type MCPCredentialInfo struct {
	ID          string    `json:"id"`
	Match       string    `json:"match,omitempty"`
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	Account     string    `json:"account"`
	Status      string    `json:"status"`
	Names       []string  `json:"names,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
	Issuer      string    `json:"issuer,omitempty"`
	CreatedNode string    `json:"created_node,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Plugins     []string  `json:"plugins"`
}

// mcpBridgeSecret is what a bridge needs to call its server; it only leaves the gateway to a same-user local CLI or the account's own mesh node.
type mcpBridgeSecret struct {
	Kind       string            `json:"kind"`
	Label      string            `json:"label"`
	Status     string            `json:"status"`
	Headers    map[string]string `json:"headers,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Generation int64             `json:"generation"`
	ExpiresAt  time.Time         `json:"expires_at,omitzero"`
	ConnectURL string            `json:"connect_url"`
}

type mcpBroker struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	fresh   map[string]mcpBridgeSecret // tokens from the refresher that replication has not delivered yet
	logins  map[string]*mcpLogin
	wake    chan struct{}
	indexed []byte
	// target finds a mesh node's address; nil means the tailnet. Tests point it at a local server.
	target func(context.Context, string) (*url.URL, error)
}

func (b *mcpBroker) lock(id string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.locks == nil {
		b.locks = map[string]*sync.Mutex{}
	}
	if b.locks[id] == nil {
		b.locks[id] = &sync.Mutex{}
	}
	return b.locks[id]
}

func (b *mcpBroker) wakeChannel() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	return b.wake
}

// credentialsChanged asks the background loop to rewrite the staging index.
func (g *Gateway) credentialsChanged() {
	select {
	case g.mcpBroker.wakeChannel() <- struct{}{}:
	default:
	}
}

func (g *Gateway) connectURL() string {
	return g.origin.Scheme + "://" + g.origin.Host + "/settings/connections"
}

const mcpCredentialColumns = "id,match,kind,label,account,client,state,created_node,created_at"

func scanMCPCredential(row interface{ Scan(...any) error }) (mcpCredential, error) {
	var credential mcpCredential
	var client, state string
	var created int64
	if err := row.Scan(&credential.ID, &credential.Match, &credential.Kind, &credential.Label, &credential.Account, &client, &state, &credential.CreatedNode, &created); err != nil {
		return credential, err
	}
	credential.CreatedAt = time.UnixMilli(created).UTC()
	if client != "" {
		if err := json.Unmarshal([]byte(client), &credential.Client); err != nil {
			return credential, fmt.Errorf("MCP credential %s has an unreadable client", credential.ID)
		}
	}
	if err := json.Unmarshal([]byte(state), &credential.State); err != nil {
		return credential, fmt.Errorf("MCP credential %s has an unreadable state", credential.ID)
	}
	return credential, nil
}

func (g *Gateway) loadMCPCredential(ctx context.Context, id string) (mcpCredential, error) {
	credential, err := scanMCPCredential(g.store.Reader.QueryRowContext(ctx, "SELECT "+mcpCredentialColumns+" FROM mcp_credentials WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return credential, errMCPCredentialNotFound
	}
	return credential, err
}

func (g *Gateway) listMCPCredentials(ctx context.Context, account string) ([]mcpCredential, error) {
	rows, err := g.store.Reader.QueryContext(ctx, "SELECT "+mcpCredentialColumns+" FROM mcp_credentials WHERE account=? ORDER BY created_at,id", account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentials []mcpCredential
	for rows.Next() {
		credential, err := scanMCPCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	approvals, err := g.store.Reader.QueryContext(ctx, "SELECT a.credential_id,a.module FROM mcp_credential_approvals a JOIN mcp_credentials c ON c.id=a.credential_id WHERE c.account=? ORDER BY a.module", account)
	if err != nil {
		return nil, err
	}
	defer approvals.Close()
	for approvals.Next() {
		var id, module string
		if err := approvals.Scan(&id, &module); err != nil {
			return nil, err
		}
		for i := range credentials {
			if credentials[i].ID == id {
				credentials[i].Plugins = append(credentials[i].Plugins, module)
			}
		}
	}
	return credentials, approvals.Err()
}

func (g *Gateway) writeMCPState(ctx context.Context, id string, state mcpCredentialState) error {
	nodeID, err := g.store.NodeID(ctx)
	if err != nil {
		return err
	}
	state.UpdatedAt, state.UpdatedBy = time.Now().UTC(), nodeID
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := g.store.DB.ExecContext(ctx, "UPDATE mcp_credentials SET state=? WHERE id=?", string(data), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return errMCPCredentialNotFound
	}
	g.credentialsChanged()
	return nil
}

// saveMCPCredential inserts a credential, or replaces the one of the same
// account, kind, URL and label in place so its ID (and staging) stays put.
func (g *Gateway) saveMCPCredential(ctx context.Context, credential mcpCredential) (string, error) {
	nodeID, err := g.store.NodeID(ctx)
	if err != nil {
		return "", err
	}
	client, err := json.Marshal(credential.Client)
	if err != nil {
		return "", err
	}
	tx, err := g.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existing string
	var state string
	err = tx.QueryRowContext(ctx, "SELECT id,state FROM mcp_credentials WHERE account=? AND kind=? AND match=? AND label=?", credential.Account, credential.Kind, credential.Match, credential.Label).Scan(&existing, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if existing != "" {
		var previous mcpCredentialState
		if json.Unmarshal([]byte(state), &previous) == nil && previous.Generation >= credential.State.Generation {
			credential.State.Generation = previous.Generation + 1
		}
		credential.ID = existing
	} else {
		credential.ID = "mcpc-" + randomToken()[:24]
	}
	credential.State.UpdatedAt, credential.State.UpdatedBy = time.Now().UTC(), nodeID
	stateData, err := json.Marshal(credential.State)
	if err != nil {
		return "", err
	}
	if existing != "" {
		_, err = tx.ExecContext(ctx, "UPDATE mcp_credentials SET client=?,state=? WHERE id=?", string(client), string(stateData), existing)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO mcp_credentials("+mcpCredentialColumns+") VALUES (?,?,?,?,?,?,?,?,?)",
			credential.ID, credential.Match, credential.Kind, credential.Label, credential.Account, string(client), string(stateData), nodeID, time.Now().UnixMilli())
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	g.credentialsChanged()
	return credential.ID, nil
}

func (g *Gateway) deleteMCPCredential(ctx context.Context, id string) error {
	tx, err := g.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_credential_approvals WHERE credential_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_credentials WHERE id=?", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	g.credentialsChanged()
	return nil
}

func (credential mcpCredential) info() MCPCredentialInfo {
	info := MCPCredentialInfo{ID: credential.ID, Match: credential.Match, Kind: credential.Kind, Label: credential.Label, Account: credential.Account,
		Status: credential.State.Status, Issuer: credential.Client.Issuer, CreatedNode: credential.CreatedNode, CreatedAt: credential.CreatedAt, Plugins: credential.Plugins}
	if info.Plugins == nil {
		info.Plugins = []string{}
	}
	for name := range credential.State.Headers {
		info.Names = append(info.Names, name)
	}
	for name := range credential.State.Env {
		info.Names = append(info.Names, name)
	}
	slices.Sort(info.Names)
	if token := credential.State.Token; token != nil {
		info.ExpiresAt = token.Expiry
	}
	return info
}

func (g *Gateway) bridgeSecret(credential mcpCredential) mcpBridgeSecret {
	secret := mcpBridgeSecret{Kind: credential.Kind, Label: credential.Label, Status: credential.State.Status, Generation: credential.State.Generation, ConnectURL: g.connectURL(),
		Headers: credential.State.Headers, Env: credential.State.Env}
	if token := credential.State.Token; token != nil && credential.Kind == mcp.CredentialOAuth {
		kind := token.TokenType
		if kind == "" || strings.EqualFold(kind, "bearer") {
			kind = "Bearer"
		}
		secret.Headers = map[string]string{"Authorization": kind + " " + token.AccessToken}
		secret.ExpiresAt = token.Expiry
	}
	g.mcpBroker.mu.Lock()
	if fresh, ok := g.mcpBroker.fresh[credential.ID]; ok && fresh.Generation > secret.Generation {
		secret = fresh
	}
	g.mcpBroker.mu.Unlock()
	return secret
}

func usable(secret mcpBridgeSecret, now time.Time) bool {
	return secret.Status != mcpStatusNeedsLogin && (secret.ExpiresAt.IsZero() || secret.ExpiresAt.Sub(now) > mcpRefreshMargin)
}

// mcpRefresher is the one node that refreshes a credential: Main, or the node that created it.
func (g *Gateway) mcpRefresher(ctx context.Context, credential mcpCredential) (refresher string, local bool, err error) {
	self, err := g.store.NodeID(ctx)
	if err != nil {
		return "", false, err
	}
	roles, err := g.store.Main(ctx)
	if err != nil {
		return "", false, err
	}
	refresher = roles.MainNode
	if refresher == "" {
		refresher = credential.CreatedNode
	}
	return refresher, refresher == "" || refresher == self, nil
}

// freshMCPSecret returns a credential ready to send, refreshing an OAuth token that expired or that failedGeneration was rejected with.
func (g *Gateway) freshMCPSecret(ctx context.Context, id string, failedGeneration int64) (mcpBridgeSecret, error) {
	credential, err := g.loadMCPCredential(ctx, id)
	if err != nil {
		return mcpBridgeSecret{}, err
	}
	secret := g.bridgeSecret(credential)
	if credential.Kind != mcp.CredentialOAuth || secret.Generation > failedGeneration && usable(secret, time.Now()) {
		return secret, nil
	}
	if secret.Status == mcpStatusNeedsLogin {
		return secret, errMCPNeedsLogin
	}
	refresher, local, err := g.mcpRefresher(ctx, credential)
	if err != nil {
		return secret, err
	}
	if !local {
		remote, err := g.remoteMCPRefresh(ctx, refresher, credential, max(failedGeneration, secret.Generation))
		if err == nil {
			g.mcpBroker.mu.Lock()
			if g.mcpBroker.fresh == nil {
				g.mcpBroker.fresh = map[string]mcpBridgeSecret{}
			}
			g.mcpBroker.fresh[id] = remote
			g.mcpBroker.mu.Unlock()
			return remote, nil
		}
		if !errors.Is(err, errMCPRefresherUnreachable) {
			return secret, err
		}
	}
	credential, err = g.refreshMCPToken(ctx, id, max(failedGeneration, secret.Generation), !local)
	if err != nil {
		return g.bridgeSecret(credential), err
	}
	return g.bridgeSecret(credential), nil
}

// refreshMCPToken refreshes on this node under the credential's lock, unless
// another refresh already moved past the failed generation. A stand-in for an
// unreachable refresher waits one sync interval for a newer token before it
// gives up on a refresh token the refresher may already have rotated.
func (g *Gateway) refreshMCPToken(ctx context.Context, id string, failedGeneration int64, standIn bool) (mcpCredential, error) {
	lock := g.mcpBroker.lock(id)
	lock.Lock()
	defer lock.Unlock()
	credential, err := g.loadMCPCredential(ctx, id)
	if err != nil {
		return credential, err
	}
	if credential.State.Generation > failedGeneration && usable(g.bridgeSecret(credential), time.Now()) {
		return credential, nil
	}
	// This node now writes its own state: count past a token the refresher handed out, and stop serving that copy.
	g.mcpBroker.mu.Lock()
	if fresh, ok := g.mcpBroker.fresh[id]; ok {
		credential.State.Generation = max(credential.State.Generation, fresh.Generation)
		delete(g.mcpBroker.fresh, id)
	}
	g.mcpBroker.mu.Unlock()
	token := credential.State.Token
	if token == nil || token.RefreshToken == "" {
		return g.markNeedsLogin(ctx, credential)
	}
	config := oauth2.Config{ClientID: credential.Client.ClientID, ClientSecret: credential.Client.ClientSecret, Scopes: credential.Client.Scopes, RedirectURL: credential.Client.RedirectURL,
		Endpoint: oauth2.Endpoint{AuthURL: credential.Client.AuthURL, TokenURL: credential.Client.TokenURL, AuthStyle: oauth2.AuthStyle(credential.Client.AuthStyle)}}
	refreshCtx, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, mcpOutboundClient()), 30*time.Second)
	defer cancel()
	// shortcut: the refresh omits RFC 8707 resource; add it if a server requires it on refresh.
	refreshed, err := config.TokenSource(refreshCtx, &oauth2.Token{RefreshToken: token.RefreshToken, Expiry: time.Unix(1, 0)}).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if !errors.As(err, &retrieve) || retrieve.Response == nil || retrieve.Response.StatusCode >= 500 {
			// Never pass the token endpoint's body on: it may echo credentials.
			return credential, errors.New("MCP token refresh failed: token endpoint unreachable")
		}
		if standIn {
			if newer, ok := g.awaitNewerMCPToken(ctx, id, credential.State.Generation); ok {
				return newer, nil
			}
		}
		return g.markNeedsLogin(ctx, credential)
	}
	state := credential.State
	state.Generation++
	state.Status = mcpStatusConnected
	next := mcpToken{AccessToken: refreshed.AccessToken, TokenType: refreshed.TokenType, RefreshToken: refreshed.RefreshToken, IssuedAt: time.Now().UTC(), Expiry: refreshed.Expiry.UTC()}
	if next.RefreshToken == "" {
		next.RefreshToken = token.RefreshToken
	}
	state.Token = &next
	if err := g.writeMCPState(ctx, id, state); err != nil {
		return credential, err
	}
	credential.State = state
	return credential, nil
}

// mcpReplicationWait is how long a stand-in waits for a rotated token to replicate; tests shorten it.
var mcpReplicationWait = 30 * time.Second

func (g *Gateway) awaitNewerMCPToken(ctx context.Context, id string, generation int64) (mcpCredential, bool) {
	deadline := time.Now().Add(mcpReplicationWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return mcpCredential{}, false
		case <-time.After(min(2*time.Second, mcpReplicationWait)):
		}
		if credential, err := g.loadMCPCredential(ctx, id); err == nil && credential.State.Generation > generation {
			return credential, true
		}
	}
	return mcpCredential{}, false
}

func (g *Gateway) markNeedsLogin(ctx context.Context, credential mcpCredential) (mcpCredential, error) {
	credential.State.Status = mcpStatusNeedsLogin
	if err := g.writeMCPState(ctx, credential.ID, credential.State); err != nil {
		return credential, err
	}
	return credential, errMCPNeedsLogin
}

func (g *Gateway) remoteMCPRefresh(ctx context.Context, node string, credential mcpCredential, failedGeneration int64) (mcpBridgeSecret, error) {
	if !g.meshPeer(ctx, node) {
		return mcpBridgeSecret{}, errMCPRefresherUnreachable
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	targetFor := g.tailnetTarget
	if g.mcpBroker.target != nil {
		targetFor = g.mcpBroker.target
	}
	target, err := targetFor(ctx, node)
	if err != nil {
		return mcpBridgeSecret{}, errMCPRefresherUnreachable
	}
	body, _ := json.Marshal(map[string]int64{"generation": failedGeneration})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.JoinPath("/api/mcp/credentials/"+credential.ID+"/refresh").String(), bytes.NewReader(body))
	if err != nil {
		return mcpBridgeSecret{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agentpack-User", credential.Account)
	request.Header.Set("traceparent", obs.TraceParent(ctx))
	client := &http.Client{Transport: tailnetMutationTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return mcpBridgeSecret{}, errMCPRefresherUnreachable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		var secret mcpBridgeSecret
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&secret); err != nil {
			return mcpBridgeSecret{}, errMCPRefresherUnreachable
		}
		secret.ConnectURL = g.connectURL()
		return secret, nil
	case response.StatusCode == http.StatusConflict:
		return mcpBridgeSecret{}, errMCPNeedsLogin
	case response.StatusCode == http.StatusNotFound && response.Header.Get("Content-Type") == "application/json":
		return mcpBridgeSecret{}, errMCPRefresherUnreachable // an older Main without this route
	default:
		return mcpBridgeSecret{}, errMCPRefresherUnreachable
	}
}

// credentialAccount is the account an actor's credentials belong to.
func credentialAccount(r *http.Request) (Actor, string, bool) {
	actor, ok := actorFrom(r.Context())
	if !ok || actor.User == "" {
		return actor, "", false
	}
	switch actor.Kind {
	case "browser":
		return actor, actor.User, true
	case "local", "peer":
		return actor, actor.User, actor.Account
	}
	return actor, "", false
}

func (g *Gateway) ownedMCPCredential(w http.ResponseWriter, r *http.Request) (mcpCredential, Actor, bool) {
	actor, account, ok := credentialAccount(r)
	if !ok {
		http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
		return mcpCredential{}, actor, false
	}
	credential, err := g.loadMCPCredential(r.Context(), r.PathValue("id"))
	if errors.Is(err, errMCPCredentialNotFound) || err == nil && credential.Account != account {
		http.Error(w, "MCP credential not found", http.StatusNotFound)
		return credential, actor, false
	}
	if err != nil {
		credentialFailure(w, r, err)
		return credential, actor, false
	}
	return credential, actor, true
}

func credentialFailure(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	switch {
	case errors.Is(err, errMCPNeedsLogin):
		http.Error(w, "MCP credential needs login", http.StatusConflict)
	case errors.Is(err, errMCPCredentialNotFound):
		http.Error(w, "MCP credential not found", http.StatusNotFound)
	default:
		http.Error(w, "MCP credential unavailable", http.StatusServiceUnavailable)
	}
}

func (g *Gateway) accountEnabled(ctx context.Context, email string) bool {
	var enabled bool
	return g.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email=? AND disabled=0)", email).Scan(&enabled) == nil && enabled
}

func (g *Gateway) registerMCPCredentials(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/credentials", func(w http.ResponseWriter, r *http.Request) {
		_, account, ok := credentialAccount(r)
		if !ok {
			http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
			return
		}
		credentials, err := g.listMCPCredentials(r.Context(), account)
		if err != nil {
			credentialFailure(w, r, err)
			return
		}
		infos := make([]MCPCredentialInfo, 0, len(credentials))
		for _, credential := range credentials {
			infos = append(infos, credential.info())
		}
		writeJSON(w, map[string]any{"credentials": infos, "callback_url": g.mcpCallbackURL()})
	})
	mux.HandleFunc("POST /api/mcp/credentials", func(w http.ResponseWriter, r *http.Request) {
		_, account, ok := credentialAccount(r)
		if !ok {
			http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
			return
		}
		var input struct {
			URL    string            `json:"url"`
			Label  string            `json:"label"`
			Kind   string            `json:"kind"`
			Values map[string]string `json:"values"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		credential, err := newStaticMCPCredential(account, input.Kind, input.URL, input.Label, input.Values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := g.saveMCPCredential(r.Context(), credential)
		if err != nil {
			credentialFailure(w, r, err)
			return
		}
		writeJSON(w, map[string]string{"id": id})
	})
	// After a machine leaves the mesh: revoke every OAuth login it may hold a copy of, so each needs a fresh sign-in.
	mux.HandleFunc("POST /api/mcp/credentials/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		_, account, ok := credentialAccount(r)
		if !ok {
			http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
			return
		}
		credentials, err := g.listMCPCredentials(r.Context(), account)
		if err != nil {
			credentialFailure(w, r, err)
			return
		}
		revoked := 0
		for _, credential := range credentials {
			if credential.Kind != mcp.CredentialOAuth || credential.State.Token == nil {
				continue
			}
			state := credential.State
			state.Generation++
			state.Status, state.Token = mcpStatusNeedsLogin, nil
			if err := g.writeMCPState(r.Context(), credential.ID, state); err != nil {
				credentialFailure(w, r, err)
				return
			}
			g.revokeMCPToken(r.Context(), credential)
			revoked++
		}
		writeJSON(w, map[string]int{"revoked": revoked})
	})
	mux.HandleFunc("DELETE /api/mcp/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		credential, _, ok := g.ownedMCPCredential(w, r)
		if !ok {
			return
		}
		if err := g.deleteMCPCredential(r.Context(), credential.ID); err != nil {
			credentialFailure(w, r, err)
			return
		}
		g.revokeMCPToken(r.Context(), credential)
		writeJSON(w, map[string]bool{"removed": true})
	})
	mux.HandleFunc("POST /api/mcp/credentials/{id}/approvals", func(w http.ResponseWriter, r *http.Request) {
		credential, _, ok := g.ownedMCPCredential(w, r)
		if !ok {
			return
		}
		var input struct {
			Module string `json:"module"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		module := strings.TrimSpace(input.Module)
		if module == "" || strings.ContainsAny(module, " \t\r\n") || obs.CredentialShaped(module) {
			http.Error(w, "a package module ID is required", http.StatusBadRequest)
			return
		}
		if _, err := g.store.DB.ExecContext(r.Context(), "INSERT OR IGNORE INTO mcp_credential_approvals(credential_id,module,created_at) VALUES (?,?,?)", credential.ID, module, time.Now().UnixMilli()); err != nil {
			credentialFailure(w, r, err)
			return
		}
		g.credentialsChanged()
		writeJSON(w, map[string]bool{"approved": true})
	})
	mux.HandleFunc("DELETE /api/mcp/credentials/{id}/approvals", func(w http.ResponseWriter, r *http.Request) {
		credential, _, ok := g.ownedMCPCredential(w, r)
		if !ok {
			return
		}
		var input struct {
			Module string `json:"module"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if _, err := g.store.DB.ExecContext(r.Context(), "DELETE FROM mcp_credential_approvals WHERE credential_id=? AND module=?", credential.ID, input.Module); err != nil {
			credentialFailure(w, r, err)
			return
		}
		g.credentialsChanged()
		writeJSON(w, map[string]bool{"removed": true})
	})
	// The secret itself goes only to this machine's CLI (the bridge) acting for the owning account.
	mux.HandleFunc("GET /api/mcp/credentials/{id}/secret", func(w http.ResponseWriter, r *http.Request) {
		credential, actor, ok := g.ownedMCPCredential(w, r)
		if !ok {
			return
		}
		if actor.Kind != "local" || !g.localRequest(r) || !g.accountEnabled(r.Context(), credential.Account) {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		secret, err := g.freshMCPSecret(r.Context(), credential.ID, 0)
		if err != nil && !errors.Is(err, errMCPNeedsLogin) {
			credentialFailure(w, r, err)
			return
		}
		writeJSON(w, secret)
	})
	// A bridge whose token was rejected, or a peer asking the refresher (Main), asks for a newer one.
	mux.HandleFunc("POST /api/mcp/credentials/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		credential, actor, ok := g.ownedMCPCredential(w, r)
		if !ok {
			return
		}
		peer, _ := r.Context().Value(replicaPeerKey{}).(string)
		allowed := actor.Kind == "local" && g.localRequest(r) || actor.Kind == "peer" && actor.Account && g.meshPeer(r.Context(), peer)
		if !allowed || !g.accountEnabled(r.Context(), credential.Account) {
			http.Error(w, "local or mesh access only", http.StatusForbidden)
			return
		}
		var input struct {
			Generation int64 `json:"generation"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		var secret mcpBridgeSecret
		var err error
		if actor.Kind == "peer" {
			// This node was asked because it is the refresher: never forward again.
			var refreshed mcpCredential
			refreshed, err = g.refreshMCPToken(r.Context(), credential.ID, input.Generation, false)
			secret = g.bridgeSecret(refreshed)
		} else {
			secret, err = g.freshMCPSecret(r.Context(), credential.ID, input.Generation)
		}
		if err != nil {
			credentialFailure(w, r, err)
			return
		}
		writeJSON(w, secret)
	})
	g.registerMCPOAuth(mux)
}

func newStaticMCPCredential(account, kind, rawURL, label string, values map[string]string) (mcpCredential, error) {
	credential := mcpCredential{Kind: kind, Account: account, Label: strings.TrimSpace(label), State: mcpCredentialState{Generation: 1, Status: mcpStatusConnected}}
	if len(values) == 0 {
		return credential, errors.New("at least one value is required")
	}
	for name, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return credential, fmt.Errorf("value for %s is empty or spans lines", name)
		}
	}
	switch kind {
	case mcp.CredentialHeader:
		match, err := mcp.CanonicalURL(rawURL)
		if err != nil {
			return credential, err
		}
		for name := range values {
			if !headerNamePattern.MatchString(name) {
				return credential, fmt.Errorf("invalid header name %q", name)
			}
		}
		credential.Match, credential.State.Headers = match, values
		if credential.Label == "" {
			parsed, _ := url.Parse(match)
			credential.Label = parsed.Host
		}
	case mcp.CredentialEnv:
		if rawURL != "" {
			return credential, errors.New("an env credential applies to local servers and takes no URL")
		}
		for name := range values {
			if !envNamePattern.MatchString(name) {
				return credential, fmt.Errorf("invalid environment variable name %q", name)
			}
		}
		if credential.Label == "" {
			return credential, errors.New("an env credential needs a label that servers name with credential = \"<label>\"")
		}
		credential.State.Env = values
	default:
		return credential, fmt.Errorf("unsupported credential kind %q; use header or env, or connect with OAuth", kind)
	}
	if obs.CredentialShaped(credential.Label) {
		return credential, errors.New("the label looks like a secret; choose a plain name")
	}
	return credential, nil
}

// writeMCPIndex lists the credentials of the account bound to this machine for staging. No secret values.
func (g *Gateway) writeMCPIndex(ctx context.Context) error {
	actor, err := g.localActor(ctx)
	if err != nil {
		if errors.Is(err, errLocalAccountUnavailable) {
			actor = Actor{}
		} else {
			return err
		}
	}
	index := mcp.CredentialIndex{Credentials: []mcp.CredentialRef{}}
	if actor.Account {
		credentials, err := g.listMCPCredentials(ctx, actor.User)
		if err != nil {
			return err
		}
		for _, credential := range credentials {
			index.Credentials = append(index.Credentials, mcp.CredentialRef{ID: credential.ID, Match: credential.Match, Label: credential.Label, Kind: credential.Kind, Plugins: credential.Plugins})
		}
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	g.mcpBroker.mu.Lock()
	same := bytes.Equal(data, g.mcpBroker.indexed)
	g.mcpBroker.mu.Unlock()
	path := filepath.Join(g.options.Home, paths.MCPCredentialIndexName)
	if same {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	if previous, err := os.ReadFile(path); err != nil || !bytes.Equal(previous, data) {
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(temporary, path); err != nil {
			return err
		}
	}
	g.mcpBroker.mu.Lock()
	g.mcpBroker.indexed = data
	g.mcpBroker.mu.Unlock()
	return nil
}

// runMCPCredentials keeps the staging index current and, on the refresher,
// renews OAuth tokens at about 80 % of their lifetime so bridges rarely wait.
func (g *Gateway) runMCPCredentials(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	wake := g.mcpBroker.wakeChannel()
	for {
		if err := g.writeMCPIndex(ctx); err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "mcp.credentials", "write index")
			event.Fail(err)
			event.End()
		}
		g.renewMCPTokens(ctx)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (g *Gateway) renewMCPTokens(ctx context.Context) {
	rows, err := g.store.Reader.QueryContext(ctx, "SELECT "+mcpCredentialColumns+" FROM mcp_credentials WHERE kind=?", mcp.CredentialOAuth)
	if err != nil {
		return
	}
	var due []mcpCredential
	now := time.Now()
	for rows.Next() {
		credential, err := scanMCPCredential(rows)
		token := credential.State.Token
		if err != nil || token == nil || token.RefreshToken == "" || token.Expiry.IsZero() || credential.State.Status == mcpStatusNeedsLogin {
			continue
		}
		if now.After(token.IssuedAt.Add(token.Expiry.Sub(token.IssuedAt) * 4 / 5)) {
			due = append(due, credential)
		}
	}
	rows.Close()
	for _, credential := range due {
		if _, local, err := g.mcpRefresher(ctx, credential); err != nil || !local || !g.accountEnabled(ctx, credential.Account) {
			continue
		}
		if _, err := g.refreshMCPToken(ctx, credential.ID, credential.State.Generation, false); err != nil && ctx.Err() == nil {
			_, event := obs.Start(ctx, "mcp.credentials", "renew token")
			event.Set("credential", credential.ID)
			event.Fail(err)
			event.End()
		}
	}
}

// mcpOutboundClient reaches MCP and OAuth servers; it refuses link-local and
// cloud metadata addresses so a hostile server cannot steer discovery there.
func mcpOutboundClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.Equal(net.ParseIP("fd00:ec2::254")) {
			return fmt.Errorf("refusing to connect to %s", host)
		}
		return nil
	}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}
