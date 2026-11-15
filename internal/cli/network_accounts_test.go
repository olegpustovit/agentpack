package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestGatewayAccountsOfflineListDisableAndReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("INSERT INTO users(email,role) VALUES ('owner@example.com','owner'),('member@example.com','member')"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("session"))
	if _, err := store.DB.Exec("INSERT INTO auth_sessions(token_hash,email,created_at,expires_at) VALUES (?,'owner@example.com',1,2)", hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO oauth_tokens(secret_hash,kind,email) VALUES ('hash','access','owner@example.com')"); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runner := Runner{Stdout: &output, Stderr: io.Discard, Stdin: strings.NewReader("cancel\n")}
	for _, args := range [][]string{{"gateway", "user", "list"}, {"gateway", "user", "disable", "member@example.com"}} {
		if code, err := runner.Execute(context.Background(), args); err != nil || code != 0 {
			t.Fatalf("args=%v code=%d err=%v", args, code, err)
		}
	}
	if !strings.Contains(output.String(), "owner@example.com\towner") || strings.Contains(output.String(), "password_hash") {
		t.Fatal(output.String())
	}
	var disabled bool
	if err := store.Reader.QueryRow("SELECT disabled FROM users WHERE email='member@example.com'").Scan(&disabled); err != nil || !disabled {
		t.Fatalf("disable did not persist: %v %v", disabled, err)
	}
	if code, err := runner.Execute(context.Background(), []string{"gateway", "reset-owner"}); err == nil || code == 0 {
		t.Fatal("reset without confirmation")
	}
	var count int
	if err := store.Reader.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 2 {
		t.Fatalf("cancelled reset changed users: %d %v", count, err)
	}
	runner.Stdin = strings.NewReader("reset\n")
	if code, err := runner.Execute(context.Background(), []string{"gateway", "reset-owner"}); err != nil || code != 0 {
		t.Fatalf("reset: %d %v", code, err)
	}
	for _, table := range []string{"users", "auth_sessions", "oauth_tokens"} {
		if err := store.Reader.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("reset retained %s: %d %v", table, count, err)
		}
	}
	for _, args := range [][]string{{"gateway", "passwd", "owner@example.com", "--password=secret"}, {"gateway", "passwd", "owner@example.com", "SENTINEL-PRIVATE-PASSWORD"}, {"gateway", "user", "passwd", "owner@example.com"}, {"gateway", "setup"}, {"gateway", "user", "add"}} {
		if code, err := runner.Execute(context.Background(), args); err == nil || code == 0 {
			t.Fatalf("accepted unsupported account command: %v", args)
		}
	}
	if code, err := runner.Execute(context.Background(), []string{"gateway", "passwd", "owner@example.com"}); err == nil || code == 0 || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-TTY password accepted: %d %v", code, err)
	}
	events, err := obs.Query(context.Background(), home, obs.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "SENTINEL-PRIVATE-PASSWORD") {
		t.Fatal("rejected password argument leaked to diagnostics")
	}
}

func TestGatewayStatusPrintsUnclaimedPublicURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("AGENTPACK_PUBLIC_URL", "https://agentpack.example")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			_, _ = w.Write([]byte(`{"claimed":false,"legacy":false,"authenticated":false,"email":""}`))
		} else {
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	if err := os.Mkdir(filepath.Join(home, "network"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"address": strings.TrimPrefix(server.URL, "http://"), "secret": strings.Repeat("s", 64)})
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runner := Runner{Stdout: &output, Stderr: io.Discard, Stdin: strings.NewReader("")}
	if code, err := runner.Execute(context.Background(), []string{"gateway", "status"}); err != nil || code != 0 {
		t.Fatalf("status: %d %v", code, err)
	}
	if !strings.Contains(output.String(), "UNCLAIMED: open https://agentpack.example now") {
		t.Fatal(output.String())
	}
}

func TestGatewayJoinClosesClaimWindowOnlyAfterConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	var output strings.Builder
	runner := Runner{Stdin: strings.NewReader("cancel\n"), Stdout: &output, Stderr: io.Discard}
	if code, err := runner.Execute(t.Context(), []string{"gateway", "join", "source"}); err == nil || code == 0 {
		t.Fatal("join accepted without confirmation")
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); !os.IsNotExist(err) {
		t.Fatalf("cancelled join created state: %v", err)
	}
	runner.Stdin = strings.NewReader("trust\n")
	if code, err := runner.Execute(t.Context(), []string{"gateway", "join", "source"}); err != nil || code != 0 {
		t.Fatalf("join: %d %v", code, err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var peer string
	if err := store.Reader.QueryRow("SELECT join_peer FROM local_node WHERE id=1").Scan(&peer); err != nil || peer != "source" {
		t.Fatalf("pending join: %q %v", peer, err)
	}
	library, err := runner.networkLibrary(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := library.Store.Close(); err != nil {
			t.Error(err)
		}
	})
	peers, err := library.Store.ListPeers()
	if err != nil || len(peers) != 1 || !peers[0].Mesh {
		t.Fatalf("mesh approval: %+v %v", peers, err)
	}
	if !strings.Contains(output.String(), "Owner claiming is disabled") {
		t.Fatal("missing join state", output.String())
	}
}

func TestGatewayWhoamiBindsAnEnabledAccountOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec("INSERT INTO users(email,role) VALUES ('member@example.com','member')"); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard}
	code, err := runner.Execute(t.Context(), []string{"gateway", "whoami", "member@example.com"})
	if err != nil || code != 0 {
		t.Fatalf("whoami: %d %v", code, err)
	}
	var email string
	if err := store.Reader.QueryRow("SELECT account_email FROM local_node WHERE id=1").Scan(&email); err != nil || email != "member@example.com" {
		t.Fatalf("binding: %q %v", email, err)
	}
}
