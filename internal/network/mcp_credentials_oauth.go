package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// An OAuth login runs on the gateway the browser talks to: the SDK discovers
// the authorization server, registers a client (pre-registered or dynamic),
// and the browser returns to /mcp-oauth/callback with the code. The stored row
// then replicates to every node.

const mcpLoginLifetime = 10 * time.Minute

type mcpLogin struct {
	id, account, state string
	returnTo           string
	authorize          chan string
	result             chan *auth.AuthorizationResult
	done               chan struct{}
	expires            time.Time
	// Set before done closes.
	credentialID string
	err          error
}

type mcpLoginInput struct {
	URL          string   `json:"url"`
	Label        string   `json:"label"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	// ReturnTo is the gateway page the browser comes back to; Settings → Connections by default.
	ReturnTo string `json:"return_to"`
}

func (g *Gateway) mcpCallbackURL() string {
	return g.preferredOrigin() + "/mcp-oauth/callback"
}

func (b *mcpBroker) addLogin(login *mcpLogin) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.logins == nil {
		b.logins = map[string]*mcpLogin{}
	}
	now := time.Now()
	for id, existing := range b.logins {
		if now.After(existing.expires) {
			delete(b.logins, id)
		}
	}
	b.logins[login.id] = login
}

func (b *mcpBroker) login(id string) *mcpLogin {
	b.mu.Lock()
	defer b.mu.Unlock()
	if login := b.logins[id]; login != nil && time.Now().Before(login.expires) {
		return login
	}
	return nil
}

// takeLoginByState hands out a pending login once, for its OAuth state.
func (b *mcpBroker) takeLoginByState(state string) *mcpLogin {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, login := range b.logins {
		if state != "" && login.state == state && time.Now().Before(login.expires) {
			login.state = ""
			return login
		}
	}
	return nil
}

func (g *Gateway) startMCPLogin(account string, input mcpLoginInput) (*mcpLogin, string, error) {
	match, err := mcp.CanonicalURL(input.URL)
	if err != nil {
		return nil, "", err
	}
	label := strings.TrimSpace(input.Label)
	if label == "" {
		parsed, _ := url.Parse(match)
		label = parsed.Host
	}
	if obs.CredentialShaped(label) {
		return nil, "", errors.New("the label looks like a secret; choose a plain name")
	}
	client := mcpOutboundClient()
	returnTo := "/settings/connections"
	// Only a path on this gateway: "//host" and "/\host" would leave it.
	if strings.HasPrefix(input.ReturnTo, "/") && !strings.HasPrefix(input.ReturnTo, "//") && !strings.HasPrefix(input.ReturnTo, "/\\") {
		returnTo = input.ReturnTo
	}
	login := &mcpLogin{id: randomToken()[:24], account: account, returnTo: returnTo, authorize: make(chan string, 1), result: make(chan *auth.AuthorizationResult, 1), done: make(chan struct{}), expires: time.Now().Add(mcpLoginLifetime)}
	var granted struct {
		config *oauth2.Config
		token  *oauth2.Token
	}
	config := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         g.mcpCallbackURL(),
		RequestRefreshToken: true,
		Client:              client,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			parsed, err := url.Parse(args.URL)
			if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" {
				return nil, errors.New("the authorization server returned an unusable authorization URL")
			}
			g.mcpBroker.mu.Lock()
			login.state = parsed.Query().Get("state")
			g.mcpBroker.mu.Unlock()
			login.authorize <- args.URL
			select {
			case result := <-login.result:
				if result == nil {
					return nil, errors.New("the authorization was declined")
				}
				return result, nil
			case <-ctx.Done():
				return nil, errors.New("the sign-in was not completed in time")
			}
		},
		NewTokenSource: func(_ context.Context, config *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			granted.config, granted.token = config, token
			return oauth2.StaticTokenSource(token), nil
		},
	}
	if input.ClientID != "" {
		config.PreregisteredClient = &oauthex.ClientCredentials{ClientID: input.ClientID}
		if input.ClientSecret != "" {
			config.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: input.ClientSecret}
		}
	} else {
		config.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{g.mcpCallbackURL()}, ClientName: "agentpack", TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		}}
	}
	if len(input.Scopes) != 0 {
		scopes := input.Scopes
		config.ScopeFilter = func([]string) []string { return scopes }
	}
	handler, err := auth.NewAuthorizationCodeHandler(config)
	if err != nil {
		return nil, "", err
	}
	g.mcpBroker.addLogin(login)
	ctx, cancel := context.WithTimeout(context.Background(), mcpLoginLifetime)
	obs.Go("gateway.mcp_login", func() {
		defer cancel()
		defer close(login.done)
		ctx, event := obs.Start(ctx, "mcp.credentials", "oauth login")
		defer event.End()
		login.err = authorizeMCP(ctx, handler, input.URL, client)
		if login.err == nil && (granted.config == nil || granted.token == nil) {
			login.err = errors.New("the authorization server returned no token")
		}
		if login.err == nil {
			credential := mcpCredential{Kind: mcp.CredentialOAuth, Match: match, Label: label, Account: account,
				Client: mcpOAuthClient{ClientID: granted.config.ClientID, ClientSecret: granted.config.ClientSecret, AuthURL: granted.config.Endpoint.AuthURL,
					TokenURL: granted.config.Endpoint.TokenURL, AuthStyle: int(granted.config.Endpoint.AuthStyle), Scopes: granted.config.Scopes, RedirectURL: granted.config.RedirectURL},
				State: mcpCredentialState{Generation: 1, Status: mcpStatusConnected, Token: &mcpToken{AccessToken: granted.token.AccessToken, TokenType: granted.token.TokenType,
					RefreshToken: granted.token.RefreshToken, IssuedAt: time.Now().UTC(), Expiry: granted.token.Expiry.UTC()}}}
			credential.Client.Issuer, credential.Client.RevocationURL = revocationEndpoint(ctx, client, credential.Client.TokenURL, credential.Client.AuthURL)
			login.credentialID, login.err = g.saveMCPCredential(ctx, credential)
		}
		if login.err != nil {
			event.Fail(login.err)
		}
	})
	select {
	case authorizeURL := <-login.authorize:
		return login, authorizeURL, nil
	case <-login.done:
		return nil, "", login.err
	case <-time.After(45 * time.Second):
		return nil, "", errors.New("the MCP server's authorization server did not respond")
	}
}

// authorizeMCP makes the unauthenticated request the SDK needs a challenge from, then runs the whole flow.
func authorizeMCP(ctx context.Context, handler *auth.AuthorizationCodeHandler, endpoint string, client *http.Client) error {
	body := []byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"agentpack","version":"1"}}}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("the MCP server is unreachable")
	}
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		response.Body.Close()
		return fmt.Errorf("the MCP server did not ask for a login (HTTP %d); if it needs a token, add it as a header credential", response.StatusCode)
	}
	if err := handler.Authorize(ctx, request, response); err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) {
			return fmt.Errorf("the authorization server refused the login (%s)", retrieve.ErrorCode)
		}
		if strings.Contains(err.Error(), "no configured client registration methods") {
			return errors.New("this server does not register clients automatically: enter the client ID of an OAuth app registered with the redirect URL shown in Settings → Connections, or add a token header")
		}
		return fmt.Errorf("OAuth login failed: %s", obs.Redact(err.Error()))
	}
	return nil
}

// revocationEndpoint looks up RFC 7009 revocation on the authorization server, best effort.
func revocationEndpoint(ctx context.Context, client *http.Client, tokenURL, authURL string) (string, string) {
	parsed, err := url.Parse(authURL)
	if err != nil {
		return "", ""
	}
	issuer := parsed.Scheme + "://" + parsed.Host
	meta, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil || meta == nil || meta.TokenEndpoint != tokenURL {
		return issuer, ""
	}
	return meta.Issuer, meta.RevocationEndpoint
}

// revokeMCPToken tells the authorization server a removed login is gone, best effort, in the background.
func (g *Gateway) revokeMCPToken(ctx context.Context, credential mcpCredential) {
	token := credential.State.Token
	if credential.Kind != mcp.CredentialOAuth || token == nil || credential.Client.RevocationURL == "" {
		return
	}
	parent := context.WithoutCancel(ctx)
	obs.Go("gateway.mcp_revoke", func() {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		ctx, event := obs.Start(ctx, "mcp.credentials", "revoke token")
		defer event.End()
		value, hint := token.RefreshToken, "refresh_token"
		if value == "" {
			value, hint = token.AccessToken, "access_token"
		}
		form := url.Values{"token": {value}, "token_type_hint": {hint}}
		if credential.Client.ClientSecret == "" {
			form.Set("client_id", credential.Client.ClientID)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, credential.Client.RevocationURL, strings.NewReader(form.Encode()))
		if err != nil {
			event.Fail(err)
			return
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if credential.Client.ClientSecret != "" {
			request.SetBasicAuth(url.QueryEscape(credential.Client.ClientID), url.QueryEscape(credential.Client.ClientSecret))
		}
		response, err := mcpOutboundClient().Do(request)
		if err != nil {
			event.Fail(errors.New("token revocation endpoint unreachable"))
			return
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			event.Fail(fmt.Errorf("token revocation answered HTTP %d", response.StatusCode))
		}
	})
}

func (g *Gateway) registerMCPOAuth(mux routeRegistrar) {
	mux.HandleFunc("POST /api/mcp/logins", func(w http.ResponseWriter, r *http.Request) {
		_, account, ok := credentialAccount(r)
		if !ok {
			http.Error(w, "an enabled account is required for MCP credentials", http.StatusForbidden)
			return
		}
		var input mcpLoginInput
		if !readJSON(w, r, &input) {
			return
		}
		login, authorizeURL, err := g.startMCPLogin(account, input)
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]string{"login": login.id, "authorize_url": authorizeURL})
	})
	mux.HandleFunc("GET /api/mcp/logins/{login}", func(w http.ResponseWriter, r *http.Request) {
		_, account, ok := credentialAccount(r)
		login := g.mcpBroker.login(r.PathValue("login"))
		if !ok || login == nil || login.account != account {
			http.Error(w, "sign-in not found or expired", http.StatusNotFound)
			return
		}
		status := map[string]string{"status": "pending"}
		select {
		case <-login.done:
			if login.err != nil {
				status = map[string]string{"status": "failed", "error": login.err.Error()}
			} else {
				status = map[string]string{"status": "connected", "credential_id": login.credentialID}
			}
		default:
		}
		writeJSON(w, status)
	})
	// The authorization server redirects the browser here. The state proves which login it is; no cookie is sent on this cross-site navigation.
	mux.HandleFunc("GET /mcp-oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		login := g.mcpBroker.takeLoginByState(query.Get("state"))
		if login == nil {
			http.Error(w, "This sign-in link expired or was already used. Start again from Settings → Connections.", http.StatusBadRequest)
			return
		}
		if query.Get("error") != "" {
			login.result <- nil
		} else {
			login.result <- &auth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state"), Iss: query.Get("iss")}
		}
		select {
		case <-login.done:
		case <-time.After(30 * time.Second):
		}
		http.Redirect(w, r, login.returnTo, http.StatusSeeOther)
	})
}
