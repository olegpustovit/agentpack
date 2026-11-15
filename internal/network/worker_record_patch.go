package network

import (
	"context"
	"database/sql"

	"github.com/olegpustovit/agentpack/internal/state"
)

func patchWorkerConversationState(ctx context.Context, db *sql.DB, id, conversationState string) error {
	_, err := db.ExecContext(ctx, "UPDATE sessions SET conversation_state=? WHERE id=? AND archived_at=0 AND node_id="+state.LocalNodeSQL+" AND conversation_state<>?", conversationState, id, conversationState)
	if err != nil {
		return err
	}
	var archived bool
	if err := db.QueryRowContext(ctx, "SELECT archived_at>0 FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL, id).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return errSessionArchived
	}
	return nil
}
