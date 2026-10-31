package network

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type GatewayMCP struct {
	gateway  *Gateway
	db       *bolt.DB
	handler  http.Handler
	mu       sync.Mutex
	requests int
	reset    time.Time
}

func (m *GatewayMCP) Close() error                                     { return m.db.Close() }
func (m *GatewayMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.handler.ServeHTTP(w, r) }

func isMCPPath(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/oauth-")
}

func (m *GatewayMCP) baseURL() string { return m.gateway.origin.Scheme + "://" + m.gateway.origin.Host }

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{code})
}

func (m *GatewayMCP) routes(mux *http.ServeMux) {
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			b := m.baseURL()
			writeJSON(w, map[string]any{"issuer": b, "authorization_endpoint": b + "/oauth/authorize", "token_endpoint": b + "/oauth/token", "registration_endpoint": b + "/oauth/register", "revocation_endpoint": b + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"agentpack"}})
		})
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"resource": m.baseURL() + "/mcp", "authorization_servers": []string{m.baseURL()}, "scopes_supported": []string{"agentpack"}, "bearer_methods_supported": []string{"header"}, "resource_name": "Agentpack"})
		})
	}
	mux.HandleFunc("POST /oauth/register", m.register)
	mux.HandleFunc("GET /oauth/authorize", m.authorize)
	mux.HandleFunc("POST /oauth/authorize", m.authorize)
	mux.HandleFunc("POST /oauth/token", m.token)
	mux.HandleFunc("POST /oauth/revoke", m.revoke)
}

func (m *GatewayMCP) limited(w http.ResponseWriter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().After(m.reset) {
		m.requests = 0
		m.reset = time.Now().Add(time.Minute)
	}
	m.requests++
	if m.requests <= 120 {
		return false
	}
	w.Header().Set("Retry-After", "60")
	oauthError(w, 429, "rate_limited")
	return true
}

func validOAuthRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 2048 && u.User == nil && u.Fragment == "" && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

func (m *GatewayMCP) register(w http.ResponseWriter, r *http.Request) {
	if m.limited(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var client oauthClient
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&client) != nil || decoder.Decode(new(any)) != io.EOF || len(client.RedirectURIs) == 0 || len(client.RedirectURIs) > 10 || len(client.ClientName) > 100 || client.TokenAuth != "" && client.TokenAuth != "none" {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	for _, uri := range client.RedirectURIs {
		if !validOAuthRedirect(uri) {
			oauthError(w, 400, "invalid_redirect_uri")
			return
		}
	}
	for _, grant := range client.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			oauthError(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, response := range client.ResponseTypes {
		if response != "code" {
			oauthError(w, 400, "invalid_client_metadata")
			return
		}
	}
	client.TokenAuth, client.GrantTypes, client.ResponseTypes = "none", []string{"authorization_code", "refresh_token"}, []string{"code"}
	id := randomToken()
	err := m.db.Update(func(tx *bolt.Tx) error {
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

var oauthConsent = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect to Agentpack</title><main class="auth-shell"><section class="auth-card"><p class="eyebrow">Agentpack connection</p><h1>Connect {{.Name}}</h1><p>This client will be able to read terminal output, type into sessions when control is released, start and stop agents, and install packages on your connected computers.</p><p>Return address: <strong>{{.Redirect}}</strong></p><form method="post" action="{{.Action}}"><input type="hidden" name="consent" value="{{.Token}}"><button class="primary" type="submit" name="decision" value="allow">Allow access</button><button type="submit" name="decision" value="deny">Cancel</button></form></section></main></html>`))

func (m *GatewayMCP) authorize(w http.ResponseWriter, r *http.Request) {
	if m.limited(w) {
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		oauthError(w, 400, "invalid_request")
		return
	}
	query := r.URL.Query()
	var client oauthRecord
	err := m.db.View(func(tx *bolt.Tx) error { var err error; client, err = oauthGet(tx, query.Get("client_id")); return err })
	if err != nil || client.Kind != "client" || client.Client == nil {
		oauthError(w, 400, "invalid_client")
		return
	}
	redirect := query.Get("redirect_uri")
	if !slices.Contains(client.Client.RedirectURIs, redirect) {
		oauthError(w, 400, "invalid_redirect_uri")
		return
	}
	challenge, err := base64.RawURLEncoding.DecodeString(query.Get("code_challenge"))
	if err != nil || len(challenge) != 32 || query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" || query.Get("scope") != "" && query.Get("scope") != "agentpack" || query.Get("resource") != "" && query.Get("resource") != m.baseURL()+"/mcp" {
		oauthError(w, 400, "invalid_request")
		return
	}
	if !m.gateway.auth.authenticated(r) {
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	cookie, _ := r.Cookie("agentpack_session")
	session := string(oauthKey(cookie.Value))
	if r.Method == http.MethodGet {
		token := randomToken()
		err := m.db.Update(func(tx *bolt.Tx) error {
			if err := oauthPrune(tx); err != nil {
				return err
			}
			return oauthPut(tx, token, oauthRecord{Kind: "consent", Session: session, Query: r.URL.RawQuery, Expires: time.Now().Add(10 * time.Minute)})
		})
		if err != nil {
			oauthError(w, 503, "authorization_unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		name := client.Client.ClientName
		if name == "" {
			name = "Your AI assistant"
		}
		_ = oauthConsent.Execute(w, struct{ Name, Redirect, Action, Token string }{name, redirect, r.URL.RequestURI(), token})
		return
	}
	if r.Header.Get("Origin") != m.baseURL() {
		oauthError(w, 403, "invalid_origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	code := randomToken()
	err = m.db.Update(func(tx *bolt.Tx) error {
		consent, err := oauthGet(tx, r.PostForm.Get("consent"))
		if err != nil || consent.Kind != "consent" || consent.Session != session || consent.Query != r.URL.RawQuery {
			return errOAuthGrant
		}
		if err := tx.Bucket(oauthBucket).Delete(oauthKey(r.PostForm.Get("consent"))); err != nil {
			return err
		}
		if r.PostForm.Get("decision") != "allow" {
			return nil
		}
		return oauthPut(tx, code, oauthRecord{Kind: "code", ClientID: query.Get("client_id"), Redirect: redirect, Challenge: query.Get("code_challenge"), Resource: m.baseURL() + "/mcp", Expires: time.Now().Add(5 * time.Minute)})
	})
	if err != nil {
		oauthError(w, 400, "invalid_consent")
		return
	}
	u, _ := url.Parse(redirect)
	params := u.Query()
	params.Set("state", query.Get("state"))
	if r.PostForm.Get("decision") == "allow" {
		params.Set("code", code)
	} else {
		params.Set("error", "access_denied")
	}
	u.RawQuery = params.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
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

func (m *GatewayMCP) token(w http.ResponseWriter, r *http.Request) {
	if m.limited(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	form := r.PostForm
	if form.Get("resource") != "" && form.Get("resource") != m.baseURL()+"/mcp" {
		oauthError(w, 400, "invalid_target")
		return
	}
	if form.Get("scope") != "" && form.Get("scope") != "agentpack" {
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
	access, refresh := randomToken(), randomToken()
	replayed := false
	expires := 3600
	err := m.db.Update(func(tx *bolt.Tx) error {
		client, err := oauthGet(tx, form.Get("client_id"))
		if err != nil || client.Kind != "client" {
			return errOAuthGrant
		}
		var record oauthRecord
		if grant == "authorization_code" {
			record, err = oauthGet(tx, form.Get("code"))
			if err != nil || record.Kind != "code" || record.ClientID != form.Get("client_id") || record.Redirect != form.Get("redirect_uri") || !validVerifier(form.Get("code_verifier")) {
				return errOAuthGrant
			}
			hash := sha256.Sum256([]byte(form.Get("code_verifier")))
			if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(hash[:])), []byte(record.Challenge)) != 1 {
				return errOAuthGrant
			}
			if err := tx.Bucket(oauthBucket).Delete(oauthKey(form.Get("code"))); err != nil {
				return err
			}
			record.Family, record.GrantExpires = randomToken(), time.Now().Add(30*24*time.Hour)
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
		if record.Resource != m.baseURL()+"/mcp" {
			return errOAuthGrant
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
	writeJSON(w, struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int    `json:"expires_in"`
		Scope   string `json:"scope"`
	}{access, refresh, "Bearer", expires, "agentpack"})
}

func (m *GatewayMCP) revoke(w http.ResponseWriter, r *http.Request) {
	if m.limited(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	err := m.db.Update(func(tx *bolt.Tx) error {
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

func (m *GatewayMCP) authenticated(r *http.Request) bool {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	valid := false
	_ = m.db.View(func(tx *bolt.Tx) error {
		record, err := oauthGet(tx, strings.TrimPrefix(value, "Bearer "))
		valid = err == nil && record.Kind == "access" && record.Resource == m.baseURL()+"/mcp"
		return nil
	})
	return valid
}
