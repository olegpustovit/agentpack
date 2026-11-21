package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshCredentialRenewsAnHourAhead(t *testing.T) {
	var grant map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&grant)
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":28800,"scope":"user:inference user:profile"}`))
	}))
	defer server.Close()
	tokenURL = server.URL
	file := func(expires time.Time) []byte {
		data, _ := json.Marshal(map[string]any{"mcpOAuth": map[string]any{"x": "kept"}, "claudeAiOauth": map[string]any{"accessToken": "old", "refreshToken": "old-refresh", "expiresAt": expires.UnixMilli(), "scopes": []string{"user:inference"}, "subscriptionType": "max"}})
		return data
	}
	if renewed, err := RefreshCredential(t.Context(), file(time.Now().Add(2*time.Hour))); renewed != nil || err != nil || grant != nil {
		t.Fatalf("renewed early: %s %v", renewed, err)
	}
	renewed, err := RefreshCredential(t.Context(), file(time.Now().Add(30*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if grant["grant_type"] != "refresh_token" || grant["refresh_token"] != "old-refresh" || grant["client_id"] != oauthClientID || grant["scope"] != "user:inference" {
		t.Fatalf("grant: %v", grant)
	}
	var got struct {
		MCP   map[string]string `json:"mcpOAuth"`
		OAuth struct {
			AccessToken, RefreshToken, SubscriptionType string
			ExpiresAt                                   int64
			Scopes                                      []string
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(renewed, &got); err != nil {
		t.Fatal(err)
	}
	if got.MCP["x"] != "kept" || got.OAuth.AccessToken != "new-access" || got.OAuth.RefreshToken != "new-refresh" || got.OAuth.SubscriptionType != "max" || len(got.OAuth.Scopes) != 2 || time.Until(time.UnixMilli(got.OAuth.ExpiresAt)) < 7*time.Hour {
		t.Fatalf("renewed file: %s", renewed)
	}
}
