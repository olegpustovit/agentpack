package network

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayBootstrapAndLogin(t *testing.T) {
	home := t.TempDir()
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string, setup bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "https://agentpack.example/api/setup", bytes.NewBufferString(body))
		auth.login(w, r, setup, true)
		return w
	}
	if w := request(`{"password":"a-long-password","token":"wrong"}`, true); w.Code != 401 {
		t.Fatalf("wrong bootstrap token: %d", w.Code)
	}
	if w := request(`{"password":"short","token":"`+auth.setup+`"}`, true); w.Code != 400 {
		t.Fatalf("weak password: %d", w.Code)
	}
	token := auth.setup
	w := request(`{"password":"a-long-password","token":"`+token+`"}`, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie", cookie)
	}
	r := httptest.NewRequest("GET", "https://agentpack.example/api/sessions", nil)
	r.AddCookie(cookie)
	if !auth.authenticated(r) {
		t.Fatal("login missing")
	}
	if _, err = os.Stat(filepath.Join(home, "network", "setup-token")); !os.IsNotExist(err) {
		t.Fatal("bootstrap token retained")
	}
	if w := request(`{"password":"replacement-password","token":"`+token+`"}`, true); w.Code != 409 {
		t.Fatal("bootstrap reused", w.Code)
	}
	auth, err = newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	if auth.authenticated(r) {
		t.Fatal("old cookie survived restart")
	}
	if w := request(`{"password":"wrong"}`, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = request(`{"password":"a-long-password"}`, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "https://agentpack.example/api/logout", nil)
	r.AddCookie(w.Result().Cookies()[0])
	auth.logout(httptest.NewRecorder(), r)
	if auth.authenticated(r) {
		t.Fatal("logout did not revoke")
	}
}

func TestGatewayLoginRateLimitAndStrictJSON(t *testing.T) {
	auth, err := newGatewayAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"password":"bad"}`))
		auth.login(w, r, false, false)
		want := 401
		if i == 10 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	for _, body := range []string{`{} {}`, `{"unknown":true}`, `{`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
		var input struct{}
		if readJSON(w, r, &input) {
			t.Fatal("accepted", body)
		}
	}
}
