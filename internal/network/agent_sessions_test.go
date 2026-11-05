package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAgentSessionsFixture(t *testing.T) {
	t.Setenv("AGENTPACK_TEST_ACP", "3")
	dir := t.TempDir()
	sessions, err := ListAgentSessions(context.Background(), t.TempDir(), dir, "claude", dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "prior-1" || sessions[0].Title != "Test" {
		t.Fatalf("got %#v", sessions)
	}
}

func TestAgentSessionsGateway(t *testing.T) {
	home := t.TempDir()
	if err := EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := remember(home, SessionInfo{Directory: directory, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	auth, err := newGatewayAuth(home)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("http://127.0.0.1:7420")
	g := &Gateway{auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	t.Setenv("AGENTPACK_TEST_ACP", "3")
	t.Cleanup(func() { t.Setenv("AGENTPACK_TEST_ACP", "") })

	path := origin.String() + "/api/agent-sessions?directory=" + url.QueryEscape(directory) + "&harness=claude"
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+g.local.Secret)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Sessions []AgentSession `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].ID != "prior-1" || body.Sessions[0].Title != "Test" {
		t.Fatalf("got %#v", body.Sessions)
	}
}

func TestAgentSessionCache(t *testing.T) {
	t.Setenv("AGENTPACK_TEST_ACP", "3")
	dir := filepath.Clean(t.TempDir())
	home := t.TempDir()
	ctx := context.Background()
	first, err := ListAgentSessions(ctx, home, dir, "claude", dir, "default")
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %v err=%v", first, err)
	}
	t.Setenv("AGENTPACK_TEST_ACP", "")
	second, err := ListAgentSessions(ctx, home, dir, "claude", dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "prior-1" {
		t.Fatalf("cache hit expected fixture row, got %#v", second)
	}
}

func TestAgentSessionProbeErrorIsNotCached(t *testing.T) {
	dir := filepath.Clean(t.TempDir())
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListAgentSessions(ctx, home, dir, "claude", dir, "default"); err == nil {
		t.Fatal("canceled session probe unexpectedly succeeded")
	}
	t.Setenv("AGENTPACK_TEST_ACP", "3")
	sessions, err := ListAgentSessions(context.Background(), home, dir, "claude", dir, "default")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("retry after canceled probe: sessions=%v err=%v", sessions, err)
	}
}
func TestACPConversationOwnerPersistsStableIdentity(t *testing.T) {
	home := t.TempDir()
	directory := t.TempDir()
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner := Actor{Kind: "peer", User: "alice@example.com", Node: "old-name", StableNodeID: "stable-peer"}
	session := SessionInfo{
		Directory: directory, FolderID: folderID, Harness: "agent", Mode: "default",
		Transport: "acp", AgentSessionID: "agent-session-1", StartedBy: &owner,
	}
	if err := rememberACPConversationOwner(home, session, ""); err != nil {
		t.Fatal(err)
	}
	got, found, err := readACPConversationOwner(home, folderID, "cursor", session.AgentSessionID)
	if err != nil || !found || got != owner {
		t.Fatalf("owner = %+v, found=%t, err=%v", got, found, err)
	}
	if _, found, err := readACPConversationOwner(home, folderID, "cursor", "other-session"); err != nil || found {
		t.Fatalf("other session owner found=%t, err=%v", found, err)
	}
}
