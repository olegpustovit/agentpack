package network

import (
	"context"
	"database/sql"
)

func patchWorkerConversationState(ctx context.Context, db *sql.DB, id, conversationState string) error {
	_, err := db.ExecContext(ctx, "UPDATE sessions SET conversation_state=? WHERE id=? AND node_id=? AND conversation_state<>?", conversationState, id, localStateNode, conversationState)
	return err
}
