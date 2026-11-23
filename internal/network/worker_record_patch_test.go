package network

import (
	"context"
	"errors"
	"testing"

	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
)

func TestPatchWorkerConversationState(t *testing.T) {
	store := statetest.Open(t, t.TempDir())
	ctx := context.Background()
	info := session.SessionInfo{ID: "session-test", ConversationState: "idle"}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PatchWorkerConversationState(ctx, store.DB, info.ID, "needs_you"); err != nil {
			t.Fatal(err)
		}
		got, err := session.StoredSession(ctx, store.Reader, info.ID)
		if err != nil || got.ConversationState != "needs_you" {
			t.Fatalf("state=%q err=%v", got.ConversationState, err)
		}
	}
	if _, err := store.DB.Exec("UPDATE sessions SET ended_at=1 WHERE id=?", info.ID); err != nil {
		t.Fatal(err)
	}
	if err := PatchWorkerConversationState(ctx, store.DB, info.ID, "working"); err != nil {
		t.Fatal(err)
	}
	got, err := session.StoredSession(ctx, store.Reader, info.ID)
	if err != nil || got.ConversationState != "needs_you" {
		t.Fatalf("ended state=%q err=%v", got.ConversationState, err)
	}
	if _, err := store.DB.Exec("UPDATE sessions SET archived_at=1 WHERE id=?", info.ID); err != nil {
		t.Fatal(err)
	}
	if err := PatchWorkerConversationState(ctx, store.DB, info.ID, "working"); !errors.Is(err, session.ErrSessionArchived) {
		t.Fatalf("revived archived worker: %v", err)
	}
}
