package network

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/state"
)

func testRememberACPConversationOwner(t *testing.T, home string, session SessionInfo, fallback string) error {
	t.Helper()
	store := testSessionStore(t, home)
	if session.ID == "" {
		session.ID = "session-" + rand.Text()
	}
	pending := session
	pending.AgentSessionID = ""
	if err := upsertSession(t.Context(), store.DB, pending); err != nil {
		return err
	}
	return rememberACPConversationOwner(t.Context(), store, session, fallback)
}

func TestNativeOwnerClaimIsAtomicAndCreatesNoFiles(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	stores := []*state.Store{testSessionStore(t, home), testSessionStore(t, home)}
	folderID, err := FolderIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	sessions := []SessionInfo{
		{ID: "session-first", Directory: directory, FolderID: folderID, Harness: "agent", Transport: "acp", StartedBy: &Actor{Kind: "browser", User: "first@example.com", Node: "host"}},
		{ID: "session-second", Directory: directory, FolderID: folderID, Harness: "cursor", Transport: "acp", StartedBy: &Actor{Kind: "browser", User: "second@example.com", Node: "host"}},
	}
	for i, session := range sessions {
		if err := upsertSession(t.Context(), stores[i].DB, session); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, session := range sessions {
		go func() {
			<-start
			session.AgentSessionID = "native-shared"
			results <- rememberACPConversationOwner(t.Context(), stores[i], session, "")
		}()
	}
	close(start)
	won, denied := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			won++
		case errors.Is(err, errACPResumeDenied):
			denied++
		default:
			t.Fatal(err)
		}
	}
	if won != 1 || denied != 1 {
		t.Fatalf("owner claims: accepted=%d denied=%d", won, denied)
	}
	owner, found, err := readACPConversationOwner(t.Context(), stores[0], folderID, "cursor", "native-shared")
	if err != nil || !found || owner.User == "" {
		t.Fatalf("owner not durable: %+v %t %v", owner, found, err)
	}
	var count int
	if err := stores[0].Reader.QueryRow("SELECT count(*) FROM sessions WHERE agent_session_id='native-shared' AND owner_email=?", owner.User).Scan(&count); err != nil || count != 1 {
		t.Fatalf("native ownership was not atomic: %d %v", count, err)
	}
	if _, err := os.Stat(filepath.Join(home, "network", "acp-session-owners")); !os.IsNotExist(err) {
		t.Fatalf("ownership files were written: %v", err)
	}
}
