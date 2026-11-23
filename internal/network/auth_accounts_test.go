package network

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/crypto/bcrypt"
)

func TestAccountValidation(t *testing.T) {
	for _, test := range []struct{ input, want string }{{" Ada@Example.COM ", "ada@example.com"}, {"a+b@example.com", "a+b@example.com"}, {"", ""}, {"not-an-email", ""}, {"Ada <ada@example.com>", ""}, {"a@example.com\r\nBcc: b@example.com", ""}, {strings.Repeat("a", 250) + "@example.com", ""}} {
		got, err := session.NormalizeEmail(test.input)
		if (err != nil) != (test.want == "") || got != test.want {
			t.Fatalf("normalize %q: %q %v", test.input, got, err)
		}
	}
	for _, test := range []struct {
		password string
		valid    bool
	}{{strings.Repeat("a", 11), false}, {strings.Repeat("a", 12), true}, {strings.Repeat("a", 72), true}, {strings.Repeat("a", 73), false}, {strings.Repeat("界", 4), true}, {strings.Repeat("界", 25), false}} {
		if (validPassword(test.password) == nil) != test.valid {
			t.Fatalf("wrong byte-length validation: %d", len(test.password))
		}
	}
	if cost, err := bcrypt.Cost([]byte(dummyPasswordHash)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("unknown-account comparison is not default-cost: %d %v", cost, err)
	}
}

func TestClaimOwnerConcurrentSingleWinner(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	type result struct {
		won bool
		err error
	}
	results := make(chan result, 20)
	for range 20 {
		go func() {
			won, err := claimOwner(context.Background(), store, "Owner@Example.COM", []byte(dummyPasswordHash))
			results <- result{won, err}
		}()
	}
	winners := 0
	for range 20 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			winners++
		}
	}
	users, err := ListUsers(context.Background(), store)
	if err != nil || winners != 1 || len(users) != 1 || users[0].Email != "owner@example.com" || users[0].Role != "owner" {
		t.Fatalf("winners=%d users=%+v err=%v", winners, users, err)
	}
	if won, err := claimOwner(context.Background(), store, "other@example.com", []byte(dummyPasswordHash)); err != nil || won {
		t.Fatalf("claimed an existing gateway: %v %v", won, err)
	}
	if _, err := claimOwner(context.Background(), store, "other@example.com", []byte("invalid")); err == nil {
		t.Fatal("accepted invalid hash")
	}
}

func TestAccountLifecycleAndSessionRevocation(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	password := "a-long-account-password"
	if err := AddUser(ctx, store, "member@example.com", password); err == nil {
		t.Fatal("created a member without an owner")
	}
	if won, err := claimOwner(ctx, store, "owner@example.com", []byte(dummyPasswordHash)); err != nil || !won {
		t.Fatalf("claim=%v %v", won, err)
	}
	if err := AddUser(ctx, store, "Member@Example.COM", password); err != nil {
		t.Fatal(err)
	}
	if err := AddUser(ctx, store, "MEMBER@example.com", password); err == nil {
		t.Fatal("duplicate normalized account")
	}
	user, err := verifyLogin(ctx, store, "MEMBER@example.com", password)
	if err != nil || user.Email != "member@example.com" || user.Role != "member" {
		t.Fatalf("login=%+v %v", user, err)
	}
	for _, test := range [][2]string{{"member@example.com", "wrong-password"}, {"unknown@example.com", password}, {"bad", password}, {"member@example.com", strings.Repeat("a", 73)}} {
		if _, err := verifyLogin(ctx, store, test[0], test[1]); !errors.Is(err, errInvalidLogin) {
			t.Fatalf("login error differs for %q: %v", test[0], err)
		}
	}
	token := sha256.Sum256([]byte("private-session-token"))
	stamp := time.Now().Add(time.Hour).UnixMilli()
	if _, err := store.DB.Exec("INSERT INTO auth_sessions(token_hash,email,created_at,expires_at) VALUES (?,?,?,?)", token[:], user.Email, stamp, stamp+3600000); err != nil {
		t.Fatal(err)
	}
	changed := "a-different-account-password"
	if err := SetPassword(ctx, store, user.Email, changed); err != nil {
		t.Fatal(err)
	}
	var tokens int
	var cutoff int64
	if err := store.Reader.QueryRow("SELECT count(*) FROM auth_sessions WHERE email=?", user.Email).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("password change retained sessions: %d %v", tokens, err)
	}
	if err := store.Reader.QueryRow("SELECT sessions_valid_after FROM users WHERE email=?", user.Email).Scan(&cutoff); err != nil || cutoff <= stamp {
		t.Fatalf("revocation did not advance past old token: %d %v", cutoff, err)
	}
	if _, err := verifyLogin(ctx, store, user.Email, password); !errors.Is(err, errInvalidLogin) {
		t.Fatalf("old password works: %v", err)
	}
	if _, err := verifyLogin(ctx, store, user.Email, changed); err != nil {
		t.Fatal(err)
	}
	if err := DisableUser(ctx, store, strings.ToUpper(user.Email)); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyLogin(ctx, store, user.Email, changed); !errors.Is(err, errInvalidLogin) {
		t.Fatalf("disabled user can log in: %v", err)
	}
	users, err := ListUsers(ctx, store)
	if err != nil || len(users) != 2 || users[0].Email != user.Email || !users[0].Disabled || users[0].SessionsValidAfter <= cutoff {
		t.Fatalf("list=%+v %v", users, err)
	}
	data, _ := json.Marshal(users)
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "$2") {
		t.Fatalf("list exposed password material: %s", data)
	}
	if err := DisableUser(ctx, store, "missing@example.com"); err == nil {
		t.Fatal("disabled a missing user")
	}
	if err := SetPassword(ctx, store, user.Email, "short"); err == nil {
		t.Fatal("accepted short password")
	}
}
