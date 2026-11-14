package network

import (
	"context"
	"testing"
)

func TestPatchWorkerConversationState(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	ctx := context.Background()
	info := SessionInfo{ID: "session-test", ConversationState: "idle"}
	if err := upsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := patchWorkerConversationState(ctx, store.DB, info.ID, "needs_you"); err != nil {
			t.Fatal(err)
		}
		got, err := storedSession(ctx, store.Reader, info.ID)
		if err != nil || got.ConversationState != "needs_you" {
			t.Fatalf("state=%q err=%v", got.ConversationState, err)
		}
	}
}
