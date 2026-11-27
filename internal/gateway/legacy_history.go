package gateway

import (
	"context"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (g *Gateway) restoreLegacyHistory(ctx context.Context) {
	ctx, event := obs.Start(ctx, "session.restore_legacy_history", "restore legacy chat history")
	if err := restoreLegacyHistory(ctx, g.store); err != nil && ctx.Err() == nil {
		event.Fail(err)
		event.End()
	}
}

func restoreLegacyHistory(ctx context.Context, store *state.Store) error {
	const key = "legacy_chat_history_restored"
	var done bool
	if err := store.Reader.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM node_settings WHERE node_id=`+state.LocalNodeSQL+` AND key=?)`, key).Scan(&done); err != nil {
		return fmt.Errorf("read chat history upgrade marker: %w", err)
	}
	if done {
		return nil
	}

	// Keep each write transaction short and leave completed batches intact on interruption.
	for {
		result, err := store.DB.ExecContext(ctx, `UPDATE sessions
 SET hidden=0, closed_at=coalesce(nullif(last_used,0),ended_at)
 WHERE id IN (
   SELECT id FROM sessions
   WHERE node_id=`+state.LocalNodeSQL+` AND hidden=1 AND transport='acp' AND ended_at>0 AND archived_at=0
     AND EXISTS (SELECT 1 FROM conversation_events WHERE session_id=sessions.id AND kind='prompt')
   LIMIT 128
 )`)
		if err != nil {
			return fmt.Errorf("restore batch of legacy chats: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count restored legacy chats: %w", err)
		}
		if count == 0 {
			break
		}
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO node_settings(node_id,key,value)
 VALUES (`+state.LocalNodeSQL+`,?,'1') ON CONFLICT(node_id,key) DO NOTHING`, key); err != nil {
		return fmt.Errorf("record chat history upgrade completion: %w", err)
	}
	return nil
}
