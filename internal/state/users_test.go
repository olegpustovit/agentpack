package state

import "testing"

func TestAccountSchemaDefaultsAndHashedSessionKey(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("INSERT INTO users(email) VALUES ('test@example.com')"); err != nil {
		t.Fatal(err)
	}
	var hash, role string
	var disabled, cutoff, created int64
	if err := store.Reader.QueryRow("SELECT password_hash,role,disabled,sessions_valid_after,created_at FROM users WHERE email='test@example.com'").Scan(&hash, &role, &disabled, &cutoff, &created); err != nil || hash != "" || role != "member" || disabled != 0 || cutoff != 0 || created != 0 {
		t.Fatalf("account defaults: hash=%q role=%q disabled=%d cutoff=%d created=%d err=%v", hash, role, disabled, cutoff, created, err)
	}
	if _, err := store.DB.Exec("INSERT INTO auth_sessions(token_hash,email,created_at,expires_at) VALUES (?, 'test@example.com', 1, 2)", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	var kind string
	var size int
	if err := store.Reader.QueryRow("SELECT typeof(token_hash),length(token_hash) FROM auth_sessions").Scan(&kind, &size); err != nil || kind != "blob" || size != 32 {
		t.Fatalf("session hash: %s %d %v", kind, size, err)
	}
	otherHash := make([]byte, 32)
	otherHash[0] = 1
	if _, err := store.DB.Exec("INSERT INTO auth_sessions(token_hash,email,created_at) VALUES (?, 'test@example.com', 1)", otherHash); err == nil {
		t.Fatal("accepted missing session expiry")
	}
}
