package network

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/obs"
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

// setTitle names a chat after its first prompt. Titles are product data like names; they never go to diagnostics.
func (worker *WorkerHTTP) setTitle(ctx context.Context, prompt string) {
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" || worker.stateStore == nil {
		return
	}
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:79]) + "…"
	}
	if _, err := worker.stateStore.DB.ExecContext(ctx, "UPDATE sessions SET title=? WHERE id=? AND title='' AND archived_at=0 AND node_id="+state.LocalNodeSQL, title, filepath.Base(worker.sessionDir)); err != nil {
		_, event := obs.Start(ctx, "worker", "set_title")
		event.Fail(err)
		event.End()
	}
}
