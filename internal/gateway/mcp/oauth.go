package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

type cachedClient struct {
	client  oauthClient
	expires time.Time
}

func (m *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.handler.ServeHTTP(w, r) }

// IsPath reports whether the MCP server, its OAuth server or their metadata answer path.
func IsPath(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/oauth-")
}

// baseURL is the origin the request addressed: loopback, or the public origin while it is on.
func (m *Server) baseURL(r *http.Request) string {
	if origin := m.host.Origin(r); origin != "" {
		return origin
	}
	return m.loopback
}

// ProtectedResourceMetadata is the RFC 9728 document for the MCP endpoint at base.
func ProtectedResourceMetadata(base string) map[string]any {
	return map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"agentpack"}, "bearer_methods_supported": []string{"header"}, "resource_name": "Agentpack"}
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{code})
}

func (m *Server) routes(mux *http.ServeMux) {
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			b := m.baseURL(r)
			network.WriteJSON(w, map[string]any{"issuer": b, "authorization_endpoint": b + "/oauth/authorize", "token_endpoint": b + "/oauth/token", "registration_endpoint": b + "/oauth/register", "revocation_endpoint": b + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"agentpack", "offline_access"}, "client_id_metadata_document_supported": true})
		})
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			network.WriteJSON(w, ProtectedResourceMetadata(m.baseURL(r)))
		})
	}
	mux.HandleFunc("POST /oauth/register", m.register)
	mux.HandleFunc("GET /oauth/authorize", m.authorize)
	mux.HandleFunc("POST /oauth/authorize", m.authorize)
	mux.HandleFunc("POST /oauth/token", m.token)
	mux.HandleFunc("POST /oauth/revoke", m.revoke)
}

// limited allows each client address 120 OAuth requests a minute, and all of them together 1200.
func (m *Server) limited(w http.ResponseWriter, r *http.Request) bool {
	m.mu.Lock()
	client, local := m.host.ClientKey(r)
	wait := m.window.Take(time.Now(), client, 120, network.LimiterCeiling(local, 1200))
	m.mu.Unlock()
	if wait == 0 {
		return false
	}
	network.RetryAfter(w, wait)
	oauthError(w, 429, "rate_limited")
	return true
}

func validOAuthRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.User == nil && u.Fragment == "" && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

func (m *Server) register(w http.ResponseWriter, r *http.Request) {
	if m.limited(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var client oauthClient
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&client) != nil || decoder.Decode(new(any)) != io.EOF {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	if problem := normalizeClient(&client); problem != "" {
		oauthError(w, 400, problem)
		return
	}
	id := network.RandomToken()
	err := state.Transaction(r.Context(), m.store.DB, func(tx *sql.Tx) error {
		if err := oauthPrune(tx); err != nil {
			return err
		}
		return oauthPut(tx, id, oauthRecord{Kind: "client", Client: &client})
	})
	if err != nil {
		oauthError(w, 503, "registration_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		ClientID string `json:"client_id"`
		oauthClient
	}{id, client})
}

const oauthConsentStyle = `
  body { margin: 0; font: 1rem/1.5 system-ui, sans-serif; }
  main { box-sizing: border-box; max-width: 38rem; margin: 2rem auto; padding: 0 1.25rem; overflow-wrap: anywhere; }
  h1 { font-size: 1.5rem; line-height: 1.25; }
  h2 { font-size: 1rem; }
  dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: .25rem 1rem; }
  dt { font-weight: 600; }
  dd { margin: 0; }
  ul { padding-inline-start: 1.25rem; }
  li + li { margin-block-start: .25rem; }
  form { display: flex; flex-wrap: wrap; gap: .5rem; }
  button { min-height: 2.75rem; padding-inline: 1rem; font: inherit; }
`

var oauthConsent = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>{{if .Token}}Connect to Agentpack{{else}}Restart authorization{{end}}</title>
  <style>` + oauthConsentStyle + `</style>
</head>
<body>
<main>
{{if .Token}}
  <h1>Connect <bdi>{{.Name}}</bdi> to Agentpack</h1>
  <dl>
    <dt>Callback host</dt><dd><bdi>{{.CallbackHost}}</bdi></dd>
    <dt>Gateway</dt><dd><bdi>{{.Gateway}}</bdi></dd>
    <dt>Signed-in account</dt><dd><bdi>{{.Email}}</bdi></dd>
  </dl>
  <h2>What this connection allows</h2>
  <ul>
    <li>Read sessions, including chat history and terminal output.</li>
    <li>Send messages and type into terminals when control is released.</li>
    <li>Start and stop sessions on connected machines.</li>
    <li>Install packages on connected machines and restart affected agents.</li>
  </ul>
  <p>In <a href="/settings/general">Settings › General</a>, choose <strong>Sign out everywhere</strong> to revoke access for all connected assistants and sign out all browsers for this account.</p>
  <form method="post" action="{{.Action}}">
    <input type="hidden" name="consent" value="{{.Token}}">
    <button type="submit" name="decision" value="deny">Cancel</button>
    <button type="submit" name="decision" value="allow">Allow access</button>
  </form>
{{else}}
  <h1>Authorization request expired or already used</h1>
  <p>Restart to review the connection and decide whether to allow access.</p>
  <p><a href="{{.Action}}">Restart authorization</a></p>
{{end}}
</main>
</body>
</html>`))

type oauthConsentPage struct {
	Name, CallbackHost, Gateway, Email, Action, Token string
}

func renderOAuthConsent(w http.ResponseWriter, r *http.Request, status int, page oauthConsentPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Browsers send Origin: null for form posts under no-referrer.
	w.Header().Set("Referrer-Policy", "same-origin")
	styleHash := sha256.Sum256([]byte(oauthConsentStyle))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; style-src 'sha256-"+base64.StdEncoding.EncodeToString(styleHash[:])+"'")
	w.WriteHeader(status)
	if err := oauthConsent.Execute(w, page); err != nil {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(fmt.Errorf("render OAuth consent: %w", err))
		}
	}
}

func (m *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if m.limited(w, r) {
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		oauthError(w, 400, "invalid_request")
		return
	}
	query := r.URL.Query()
	var client oauthRecord
	var err error
	if clientMetadataURL(query.Get("client_id")) {
		client.Kind = "client"
		client.Client, err = m.metadataClient(r.Context(), query.Get("client_id"))
	} else {
		err = state.Transaction(r.Context(), m.store.Reader, func(tx *sql.Tx) error { var err error; client, err = oauthGet(tx, query.Get("client_id")); return err })
	}
	if err != nil || client.Kind != "client" || client.Client == nil {
		oauthError(w, 400, "invalid_client")
		return
	}
	redirect := query.Get("redirect_uri")
	redirectURL, err := url.Parse(redirect)
	if err != nil || !redirectRegistered(client.Client.RedirectURIs, redirect) {
		oauthError(w, 400, "invalid_redirect_uri")
		return
	}
	challenge, err := base64.RawURLEncoding.DecodeString(query.Get("code_challenge"))
	if err != nil || len(challenge) != 32 || query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" || !validScope(query.Get("scope")) || query.Get("resource") != "" && query.Get("resource") != m.baseURL(r)+"/mcp" {
		oauthError(w, 400, "invalid_request")
		return
	}
	action := "/oauth/authorize?" + r.URL.RawQuery
	login, ok := m.host.BrowserLogin(r)
	if !ok {
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(action), http.StatusSeeOther)
		return
	}
	sessionKey := string(oauthKey(login))
	cookieHash := sha256.Sum256([]byte(login))
	if r.Method == http.MethodGet {
		token := network.RandomToken()
		record := oauthRecord{Kind: "consent", Session: sessionKey, Query: r.URL.RawQuery, Expires: time.Now().Add(10 * time.Minute)}
		err := state.Transaction(r.Context(), m.store.DB, func(tx *sql.Tx) error {
			if err := oauthPrune(tx); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT s.email,u.sessions_valid_after FROM auth_sessions s JOIN users u ON u.email=s.email WHERE s.token_hash=? AND s.expires_at>? AND u.disabled=0 AND s.created_at>=u.sessions_valid_after AND s.session_epoch=u.sessions_valid_after`, cookieHash[:], time.Now().UnixMilli()).Scan(&record.Email, &record.SessionEpoch); err != nil {
				return err
			}
			return oauthPut(tx, token, record)
		})
		if err != nil {
			oauthError(w, 503, "authorization_unavailable")
			return
		}
		name := client.Client.ClientName
		if clientMetadataURL(query.Get("client_id")) {
			// A metadata document names itself; only its host is vouched for by HTTPS.
			if parsed, err := url.Parse(query.Get("client_id")); err == nil {
				name = parsed.Host
			}
		}
		if name == "" {
			name = "Your AI assistant"
		}
		renderOAuthConsent(w, r, http.StatusOK, oauthConsentPage{
			Name: name, CallbackHost: redirectURL.Host, Gateway: session.LocalHostname(), Email: record.Email, Action: action, Token: token,
		})
		return
	}
	if r.Header.Get("Origin") != m.baseURL(r) {
		oauthError(w, 403, "invalid_origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	code := network.RandomToken()
	err = state.Transaction(r.Context(), m.store.DB, func(tx *sql.Tx) error {
		consent, err := oauthGet(tx, r.PostForm.Get("consent"))
		if err != nil {
			return err
		}
		if consent.Kind != "consent" || consent.Session != sessionKey || consent.Query != r.URL.RawQuery {
			return errOAuthGrant
		}
		if err := oauthAccountValid(tx, consent); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM oauth_tokens WHERE secret_hash = ?", string(oauthKey(r.PostForm.Get("consent")))); err != nil {
			return err
		}
		if r.PostForm.Get("decision") != "allow" {
			return nil
		}
		return oauthPut(tx, code, oauthRecord{Kind: "code", ClientID: query.Get("client_id"), Redirect: redirect, Challenge: query.Get("code_challenge"), Resource: m.baseURL(r) + "/mcp", Expires: time.Now().Add(5 * time.Minute), Email: consent.Email, SessionEpoch: consent.SessionEpoch})
	})
	if errors.Is(err, errOAuthGrant) {
		renderOAuthConsent(w, r, http.StatusBadRequest, oauthConsentPage{Action: action})
		return
	}
	if err != nil {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(fmt.Errorf("record OAuth consent: %w", err))
		}
		oauthError(w, 400, "invalid_consent")
		return
	}
	params := redirectURL.Query()
	params.Set("state", query.Get("state"))
	if r.PostForm.Get("decision") == "allow" {
		params.Set("code", code)
	} else {
		params.Set("error", "access_denied")
	}
	redirectURL.RawQuery = params.Encode()
	http.Redirect(w, r, redirectURL.String(), http.StatusSeeOther)
}

func validVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, char := range verifier {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", char)) {
			return false
		}
	}
	return true
}

func (m *Server) token(w http.ResponseWriter, r *http.Request) {
	if m.limited(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	form := r.PostForm
	if form.Get("resource") != "" && form.Get("resource") != m.baseURL(r)+"/mcp" {
		oauthError(w, 400, "invalid_target")
		return
	}
	if !validScope(form.Get("scope")) {
		oauthError(w, 400, "invalid_scope")
		return
	}
	if form.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
		oauthError(w, 400, "invalid_client")
		return
	}
	grant := form.Get("grant_type")
	if grant != "authorization_code" && grant != "refresh_token" {
		oauthError(w, 400, "unsupported_grant_type")
		return
	}
	access, refresh := network.RandomToken(), network.RandomToken()
	replayed := false
	expires := 3600
	err := state.Transaction(r.Context(), m.store.DB, func(tx *sql.Tx) error {
		// A metadata-document client was checked at authorization; its codes and tokens name it.
		if !clientMetadataURL(form.Get("client_id")) {
			client, err := oauthGet(tx, form.Get("client_id"))
			if err != nil || client.Kind != "client" {
				return errOAuthGrant
			}
		}
		var record oauthRecord
		var err error
		if grant == "authorization_code" {
			record, err = oauthGet(tx, form.Get("code"))
			if err != nil || record.Kind != "code" || record.ClientID != form.Get("client_id") || record.Redirect != form.Get("redirect_uri") || !validVerifier(form.Get("code_verifier")) {
				return errOAuthGrant
			}
			hash := sha256.Sum256([]byte(form.Get("code_verifier")))
			if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(hash[:])), []byte(record.Challenge)) != 1 {
				return errOAuthGrant
			}
			if _, err := tx.Exec("DELETE FROM oauth_tokens WHERE secret_hash = ?", string(oauthKey(form.Get("code")))); err != nil {
				return err
			}
			record.Family, record.GrantExpires = network.RandomToken(), time.Now().Add(30*24*time.Hour)
		} else {
			record, err = oauthGet(tx, form.Get("refresh_token"))
			if err != nil || record.ClientID != form.Get("client_id") || record.Kind != "refresh" && record.Kind != "spent" {
				return errOAuthGrant
			}
			if record.Kind == "spent" {
				replayed = true
				return oauthRevokeFamily(tx, record.Family)
			}
			record.Kind = "spent"
			if err := oauthPut(tx, form.Get("refresh_token"), record); err != nil {
				return err
			}
		}
		if record.Resource != m.baseURL(r)+"/mcp" {
			return errOAuthGrant
		}
		if err := oauthAccountValid(tx, record); err != nil {
			return err
		}
		if err := oauthPrune(tx); err != nil {
			return err
		}
		record.Kind, record.Expires = "access", time.Now().Add(time.Hour)
		record.Challenge = ""
		if record.Expires.After(record.GrantExpires) {
			record.Expires = record.GrantExpires
		}
		expires = max(0, int(time.Until(record.Expires).Seconds()))
		if err := oauthPut(tx, access, record); err != nil {
			return err
		}
		record.Kind, record.Expires = "refresh", record.GrantExpires
		return oauthPut(tx, refresh, record)
	})
	if err != nil || replayed {
		oauthError(w, 400, "invalid_grant")
		return
	}
	network.WriteJSON(w, struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int    `json:"expires_in"`
		Scope   string `json:"scope"`
	}{access, refresh, "Bearer", expires, "agentpack"})
}

func (m *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if m.limited(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	err := state.Transaction(r.Context(), m.store.DB, func(tx *sql.Tx) error {
		record, err := oauthGet(tx, r.PostForm.Get("token"))
		if err != nil {
			return nil
		}
		if client := r.PostForm.Get("client_id"); client != "" && client != record.ClientID {
			return nil
		}
		return oauthRevokeFamily(tx, record.Family)
	})
	if err != nil {
		oauthError(w, 503, "revocation_unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Authenticate returns r carrying the actor of its valid MCP access token, or nil.
func (m *Server) Authenticate(r *http.Request) *http.Request {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return nil
	}
	var email string
	err := state.Transaction(r.Context(), m.store.Reader, func(tx *sql.Tx) error {
		record, err := oauthGet(tx, strings.TrimPrefix(value, "Bearer "))
		if err != nil {
			return err
		}
		if record.Kind != "access" || record.Resource != m.baseURL(r)+"/mcp" {
			return errOAuthGrant
		}
		if err := oauthAccountValid(tx, record); err != nil {
			return err
		}
		email = record.Email
		return nil
	})
	if err != nil {
		if !errors.Is(err, errOAuthGrant) && r.Context().Err() == nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
		return nil
	}
	return r.WithContext(session.WithActor(r.Context(), session.Actor{Kind: "mcp", User: email, Node: session.LocalHostname()}))
}

// normalizeClient checks registered or published client metadata and makes it a public client:
// any token_endpoint_auth_method is downgraded to none, because this server issues no secrets.
func normalizeClient(client *oauthClient) string {
	if len(client.RedirectURIs) == 0 || len(client.RedirectURIs) > 10 || len(client.ClientName) > 100 {
		return "invalid_client_metadata"
	}
	for _, uri := range client.RedirectURIs {
		if !validOAuthRedirect(uri) {
			return "invalid_redirect_uri"
		}
	}
	for _, grant := range client.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			return "invalid_client_metadata"
		}
	}
	for _, response := range client.ResponseTypes {
		if response != "code" {
			return "invalid_client_metadata"
		}
	}
	client.TokenAuth, client.GrantTypes, client.ResponseTypes = "none", []string{"authorization_code", "refresh_token"}, []string{"code"}
	return ""
}

// validScope accepts a set of the scopes this server knows, in any order.
func validScope(scope string) bool {
	for _, item := range strings.Fields(scope) {
		if item != "agentpack" && item != "offline_access" {
			return false
		}
	}
	return true
}

// redirectRegistered matches a registered redirect exactly, or a loopback one on any port (RFC 8252 §7.3).
func redirectRegistered(registered []string, redirect string) bool {
	if slices.Contains(registered, redirect) {
		return true
	}
	u, err := url.Parse(redirect)
	if err != nil || !validOAuthRedirect(redirect) || u.Scheme != "http" {
		return false
	}
	for _, raw := range registered {
		known, err := url.Parse(raw)
		if err == nil && known.Scheme == "http" && known.Hostname() == u.Hostname() && known.Path == u.Path && known.RawQuery == u.RawQuery {
			return true
		}
	}
	return false
}

// clientMetadataURL reports a URL client_id: a Client ID Metadata Document the client publishes.
func clientMetadataURL(id string) bool { return strings.HasPrefix(id, "https://") }

const clientMetadataLimit = 16 << 10

// Ranges a public-looking address can still reach private networks through.
var privateRoutes = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("2002::/16")}

// publicAddressOnly runs after DNS resolution, so a name that resolves to this machine, the LAN
// or the tailnet is refused at connect time.
func publicAddressOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	ip = ip.Unmap()
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || slices.ContainsFunc(privateRoutes, func(route netip.Prefix) bool { return route.Contains(ip) }) {
		return errors.New("client metadata host is not a public address")
	}
	return nil
}

var metadataFetcher = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, Control: publicAddressOnly}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
	},
}

// metadataClient fetches and caches a Client ID Metadata Document. The document must name its
// own URL as client_id and carry no secret.
func (m *Server) metadataClient(ctx context.Context, id string) (*oauthClient, error) {
	u, err := url.Parse(id)
	if err != nil || len(id) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Path == "" || u.Path == "/" || slices.ContainsFunc(strings.Split(u.Path, "/"), func(part string) bool { return part == "." || part == ".." }) {
		return nil, errOAuthGrant
	}
	now := time.Now()
	m.mu.Lock()
	cached, ok := m.metadata[id]
	fetcher := m.Fetcher
	fresh := ok && now.Before(cached.expires)
	// The authorize page fetches before sign-in, so the gateway must not become anyone's request cannon.
	busy := !fresh && m.fetches.Take(now, u.Hostname(), 10, 60) > 0
	m.mu.Unlock()
	if fresh {
		return &cached.client, nil
	}
	if busy {
		return nil, errOAuthGrant
	}
	if fetcher == nil {
		fetcher = metadataFetcher
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := fetcher.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, clientMetadataLimit+1))
	if err != nil || response.StatusCode != http.StatusOK || len(data) > clientMetadataLimit {
		return nil, errOAuthGrant
	}
	var document struct {
		oauthClient
		ClientID string `json:"client_id"`
		Secret   string `json:"client_secret"`
	}
	if json.Unmarshal(data, &document) != nil || document.ClientID != id || document.Secret != "" || normalizeClient(&document.oauthClient) != "" {
		return nil, errOAuthGrant
	}
	m.mu.Lock()
	if m.metadata == nil || len(m.metadata) >= 256 {
		m.metadata = map[string]cachedClient{}
	}
	m.metadata[id] = cachedClient{document.oauthClient, now.Add(time.Hour)}
	m.mu.Unlock()
	return &document.oauthClient, nil
}
