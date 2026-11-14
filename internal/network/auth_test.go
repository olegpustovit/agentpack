package network

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/crypto/bcrypt"
)

func authRequest(auth *gatewayAuth, action, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://agentpack.example/api/"+action, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	switch action {
	case "claim":
		auth.claim(w, r)
	case "login":
		auth.login(w, r)
	case "logout":
		auth.logout(w, r)
	case "logout-all":
		auth.logoutAll(w, r)
	}
	return w
}

func TestPersistentBrowserLoginCookiesAndExpiry(t *testing.T) {
	home := t.TempDir()
	auth, err := testUnclaimedAuth(t, home, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	auth.now = func() time.Time { return now }
	w := authRequest(auth, "claim", `{"email":"Owner@Example.COM","password":"a-long-password"}`, nil)
	if w.Code != 200 {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "__Host-agentpack_session" || cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || !cookie.Expires.Equal(now.Add(browserSessionLifetime).Truncate(time.Second)) {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	var stored []byte
	if err := auth.store.Reader.QueryRow("SELECT token_hash FROM auth_sessions").Scan(&stored); err != nil || !bytes.Equal(stored, hash[:]) {
		t.Fatalf("token was not stored hashed: %x %v", stored, err)
	}
	if err := auth.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	auth, err = newGatewayAuth(home, reopened, true)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now }
	r := httptest.NewRequest("GET", "https://agentpack.example/api/status", nil)
	r.AddCookie(cookie)
	if !auth.authenticated(r) {
		t.Fatal("cookie lost across restart")
	}
	now = now.Add(25 * 24 * time.Hour)
	if !auth.authenticated(r) {
		t.Fatal("cookie expired before thirty days")
	}
	var expires int64
	if err := auth.store.Reader.QueryRow("SELECT expires_at FROM auth_sessions").Scan(&expires); err != nil || expires != now.Add(browserSessionLifetime).UnixMilli() {
		t.Fatalf("sliding expiry=%d %v", expires, err)
	}
	now = now.Add(31 * 24 * time.Hour)
	if auth.authenticated(r) {
		t.Fatal("expired cookie accepted")
	}
	w = authRequest(auth, "login", `{"email":"owner@example.com","password":"a-long-password"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	if w = authRequest(auth, "logout", "", cookie); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("logout: %d %+v", w.Code, w.Result().Cookies())
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	if auth.authenticated(r) {
		t.Fatal("logged-out cookie accepted")
	}
}

func TestBrowserSessionRevocationAndUserIsolation(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, auth, testBrowserEmail, "first")
	testBrowserSession(t, auth, testBrowserEmail, "second")
	testBrowserSession(t, auth, "other@example.com", "other")
	request := func(token string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: auth.cookieName(), Value: token})
		return r
	}
	if w := authRequest(auth, "logout-all", "", &http.Cookie{Name: auth.cookieName(), Value: "first"}); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if auth.authenticated(request("first")) || auth.authenticated(request("second")) || !auth.authenticated(request("other")) {
		t.Fatal("sign out everywhere crossed users or retained a token")
	}
	testBrowserSession(t, auth, "disabled@example.com", "disabled")
	if _, err := auth.store.DB.Exec("UPDATE users SET disabled=1 WHERE email='disabled@example.com'"); err != nil {
		t.Fatal(err)
	}
	if auth.authenticated(request("disabled")) {
		t.Fatal("disabled user accepted")
	}
	testBrowserSession(t, auth, "revoked@example.com", "revoked")
	if _, err := auth.store.DB.Exec("UPDATE users SET sessions_valid_after=(SELECT created_at+1 FROM auth_sessions WHERE email=users.email) WHERE email='revoked@example.com'"); err != nil {
		t.Fatal(err)
	}
	if auth.authenticated(request("revoked")) {
		t.Fatal("replicated revocation stamp ignored")
	}
	var version User
	if err := auth.store.Reader.QueryRow("SELECT email,sessions_valid_after FROM users WHERE email=?", testBrowserEmail).Scan(&version.Email, &version.SessionsValidAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.store.DB.Exec("UPDATE users SET sessions_valid_after=sessions_valid_after+1 WHERE email=?", version.Email); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	auth.issueSession(w, request(""), version)
	if w.Code != 401 {
		t.Fatalf("stale password verification minted a new token: %d", w.Code)
	}
}

func TestLegacyClaimRequiresCurrentPasswordAndKeepsHash(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "network")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"root-password": hash, "setup-token": []byte(strings.Repeat("a", 64))} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := testUnclaimedAuth(t, home, false)
	if err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	auth.status(status, httptest.NewRequest("GET", "/api/status", nil))
	var body struct{ Claimed, Legacy bool }
	if json.Unmarshal(status.Body.Bytes(), &body) != nil || body.Claimed || !body.Legacy {
		t.Fatalf("legacy status %s", status.Body.String())
	}
	for _, current := range []string{"", "wrong-password"} {
		w := authRequest(auth, "claim", `{"email":"owner@example.com","password":"a-new-password","current_password":"`+current+`"}`, nil)
		if w.Code != 401 {
			t.Fatalf("legacy claim without proof: %d", w.Code)
		}
	}
	w := authRequest(auth, "claim", `{"email":"owner@example.com","password":"a-new-password","current_password":"legacy-password"}`, nil)
	if w.Code != 200 {
		t.Fatalf("legacy claim: %d %s", w.Code, w.Body.String())
	}
	var saved string
	if err := auth.store.Reader.QueryRow("SELECT password_hash FROM users").Scan(&saved); err != nil || saved != string(hash) {
		t.Fatalf("legacy hash changed: %v", err)
	}
	for _, name := range []string{"root-password", "setup-token"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy file retained: %s %v", name, err)
		}
	}
	if w := authRequest(auth, "login", `{"email":"owner@example.com","password":"legacy-password"}`, nil); w.Code != 200 {
		t.Fatalf("legacy password not preserved: %d", w.Code)
	}
}

func TestLoginLimiterUsesEmailAddressAndFakeClock(t *testing.T) {
	auth, err := testGatewayAuth(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	auth.now = func() time.Time { return now }
	req := func(address string) *http.Request {
		r := httptest.NewRequest("POST", "/api/login", nil)
		r.RemoteAddr = address
		return r
	}
	original := req("192.0.2.1:1234")
	for range 5 {
		w := httptest.NewRecorder()
		if !auth.allowLogin(w, original, testBrowserEmail) {
			t.Fatal("early backoff")
		}
		auth.failedLogin(original, testBrowserEmail)
	}
	for _, test := range []struct{ email, address string }{{testBrowserEmail, "192.0.2.2:99"}, {"other@example.com", "192.0.2.1:9000"}} {
		w := httptest.NewRecorder()
		if auth.allowLogin(w, req(test.address), test.email) || w.Code != 429 || w.Header().Get("Retry-After") != "1" {
			t.Fatalf("missing independent backoff: %d %v", w.Code, w.Header())
		}
	}
	now = now.Add(time.Second)
	if !auth.allowLogin(httptest.NewRecorder(), original, testBrowserEmail) {
		t.Fatal("backoff did not expire")
	}
	auth.failedLogin(original, testBrowserEmail)
	w := httptest.NewRecorder()
	if auth.allowLogin(w, original, testBrowserEmail) || w.Header().Get("Retry-After") != "2" {
		t.Fatal("backoff did not double")
	}
	for range 10 {
		auth.failedLogin(original, testBrowserEmail)
	}
	w = httptest.NewRecorder()
	if auth.allowLogin(w, original, testBrowserEmail) || w.Header().Get("Retry-After") != "900" {
		t.Fatalf("backoff cap: %v", w.Header())
	}
	now = now.Add(31 * time.Minute)
	for range 10 {
		if !auth.allowLogin(httptest.NewRecorder(), req("192.0.2.3:1"), "fresh@example.com") {
			t.Fatal("fresh requests blocked")
		}
	}
	w = httptest.NewRecorder()
	if auth.allowLogin(w, req("192.0.2.4:1"), "another@example.com") || w.Code != 429 {
		t.Fatal("global limit lost")
	}
}

func TestClaimValidationAndStrictJSON(t *testing.T) {
	auth, err := testUnclaimedAuth(t, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"email":"bad","password":"a-long-password"}`, `{"email":"owner@example.com","password":"short"}`, `{} {}`, `{"unknown":true}`, `{`} {
		if w := authRequest(auth, "claim", body, nil); w.Code != 400 {
			t.Fatalf("accepted invalid claim %q: %d", body, w.Code)
		}
	}
	if w := authRequest(auth, "claim", `{"email":"owner@example.com","password":"a-long-password"}`, nil); w.Code != 200 {
		t.Fatalf("claim: %d", w.Code)
	}
	if w := authRequest(auth, "claim", `{"email":"other@example.com","password":"a-long-password"}`, nil); w.Code != 409 {
		t.Fatal("claimed twice", w.Code)
	}
	if w := authRequest(auth, "login", `{"email":"owner@example.com","password":"wrong-password"}`, nil); w.Code != 401 {
		t.Fatal("wrong password accepted", w.Code)
	}
}

func TestUnclaimedGatewayLockdown(t *testing.T) {
	home := t.TempDir()
	auth, err := testUnclaimedAuth(t, home, false)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://agentpack.example")
	g := &Gateway{auth: auth, store: auth.store, origin: origin, options: GatewayOptions{Home: home, Assets: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ui")) })}, local: nodeRecord{Secret: strings.Repeat("a", 64)}}
	handler := g.Handler()
	for _, path := range []string{"/api/nodes", "/api/config", "/api/logs/issues", "/api/identity", "/mcp", "/.well-known/oauth-authorization-server/mcp"} {
		r := httptest.NewRequest("GET", origin.String()+path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 || strings.TrimSpace(w.Body.String()) != "unclaimed" {
			t.Fatalf("unclaimed access %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/healthz", "/api/status", "/login", "/assets/app.js"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", origin.String()+path, nil))
		if w.Code != 200 {
			t.Fatalf("bootstrap blocked %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", origin.String()+"/api/settings", nil)
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("local secret blocked: %d %s", w.Code, w.Body.String())
	}
}
