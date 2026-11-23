package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

func TestOAuthRegisteredClientDoesNotExpire(t *testing.T) {
	m := testServer(t)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("POST", testOrigin+"/oauth/register", strings.NewReader(`{"client_name":"Test assistant","redirect_uris":["https://assistant.example/callback"],"token_endpoint_auth_method":"none"}`)))
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &registered) != nil {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	err := state.Transaction(context.Background(), m.store.DB, func(tx *sql.Tx) error {
		client, err := oauthGet(tx, registered.ClientID)
		if err != nil {
			return err
		}
		if !client.Expires.IsZero() {
			t.Fatal("registered client has a time limit")
		}
		// Only credentials expire: clients registered once must survive pruning.
		if err := oauthPut(tx, "expired-access", oauthRecord{Kind: "access", Expires: time.Now().Add(-time.Hour)}); err != nil {
			return err
		}
		if err := oauthPrune(tx); err != nil {
			return err
		}
		if _, err := oauthGet(tx, "expired-access"); err == nil {
			t.Fatal("expired access token survived")
		}
		_, err = oauthGet(tx, registered.ClientID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetadataFetchesReachOnlyPublicAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "10.1.2.3:443", "100.101.102.103:443", "[fd7a:115c:a1e0::1]:443", "169.254.169.254:80", "[::ffff:127.0.0.1]:443", "[64:ff9b::a00:1]:443", "[2002:a00:1::1]:443"} {
		if publicAddressOnly("tcp", address, nil) == nil {
			t.Fatalf("metadata fetch may reach %s", address)
		}
	}
	if err := publicAddressOnly("tcp", "93.184.215.14:443", nil); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthLimitIsPerClient(t *testing.T) {
	m := testServer(t)
	limited := func(address string) bool {
		r := httptest.NewRequest("POST", "/oauth/token", nil)
		r.RemoteAddr = address
		return m.limited(httptest.NewRecorder(), r)
	}
	for range 120 {
		limited("203.0.113.1:1")
	}
	if !limited("203.0.113.1:2") || limited("203.0.113.2:1") {
		t.Fatal("OAuth limit is not per client address")
	}
}
