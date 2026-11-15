package network

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

const testBrowserEmail = "owner@example.com"

func testUnclaimedAuth(t *testing.T, home string, secure bool) (*gatewayAuth, error) {
	t.Helper()
	store, err := state.Open(home)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = store.Close() })
	return newGatewayAuth(home, store, secure)
}

func testGatewayAuth(t *testing.T, home string) (*gatewayAuth, error) {
	t.Helper()
	auth, err := testUnclaimedAuth(t, home, false)
	if err != nil {
		return nil, err
	}
	_, err = auth.store.DB.Exec("INSERT OR IGNORE INTO users(email,password_hash,role) VALUES (?,?,'owner')", testBrowserEmail, dummyPasswordHash)
	return auth, err
}

func testBrowserSession(t *testing.T, auth *gatewayAuth, email, token string) {
	t.Helper()
	if _, err := auth.store.DB.Exec("INSERT OR IGNORE INTO users(email,password_hash) VALUES (?,?)", email, dummyPasswordHash); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	now := auth.now()
	if _, err := auth.store.DB.Exec("INSERT INTO auth_sessions(token_hash,email,created_at,expires_at,session_epoch) SELECT ?,email,max(?,sessions_valid_after),?,sessions_valid_after FROM users WHERE email=?", hash[:], now.UnixMilli(), now.Add(time.Hour).UnixMilli(), email); err != nil {
		t.Fatal(err)
	}
}
