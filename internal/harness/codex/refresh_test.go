package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshCredentialRenewsADayBeforeCodex(t *testing.T) {
	var grant map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&grant)
		_, _ = w.Write([]byte(`{"id_token":"new.id.token","access_token":"new-access","refresh_token":"new-refresh"}`))
	}))
	defer server.Close()
	tokenURL = server.URL
	file := func(last time.Time) []byte {
		data, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "last_refresh": last.Format(time.RFC3339Nano), "tokens": map[string]any{"id_token": "old.id.token", "access_token": "old", "refresh_token": "old-refresh", "account_id": "account"}})
		return data
	}
	if renewed, err := RefreshCredential(t.Context(), file(time.Now().Add(-6*24*time.Hour))); renewed != nil || err != nil || grant != nil {
		t.Fatalf("renewed early: %s %v", renewed, err)
	}
	renewed, err := RefreshCredential(t.Context(), file(time.Now().Add(-7*24*time.Hour-time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if grant["grant_type"] != "refresh_token" || grant["refresh_token"] != "old-refresh" || grant["client_id"] != oauthClientID {
		t.Fatalf("grant: %v", grant)
	}
	var got struct {
		AuthMode    string    `json:"auth_mode"`
		LastRefresh time.Time `json:"last_refresh"`
		Tokens      map[string]string
	}
	if err := json.Unmarshal(renewed, &got); err != nil {
		t.Fatal(err)
	}
	// Codex reads id_token as the raw JWT string.
	if got.AuthMode != "chatgpt" || got.Tokens["id_token"] != "new.id.token" || got.Tokens["access_token"] != "new-access" || got.Tokens["refresh_token"] != "new-refresh" || got.Tokens["account_id"] != "account" || time.Since(got.LastRefresh) > time.Minute {
		t.Fatalf("renewed file: %s", renewed)
	}
}

// Shared sign-ins must land where a plain `codex` reads them, even on a machine that never ran it.
func TestCredentialFileIsNativeAuthWithoutOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	if got, want := CredentialFile(), filepath.Join(home, ".codex", "auth.json"); got != want {
		t.Fatalf("CredentialFile() = %q, want %q", got, want)
	}
}
