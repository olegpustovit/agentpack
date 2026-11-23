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
	return state.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET conversation_state=? WHERE id=? AND archived_at=0 AND ended_at=0 AND node_id="+state.LocalNodeSQL+" AND conversation_state<>?", conversationState, id, conversationState)
		if err != nil {
			return err
		}
		var archived bool
		if err := tx.QueryRowContext(ctx, "SELECT archived_at>0 FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL, id).Scan(&archived); err != nil {
			return err
		}
		if archived {
			return errSessionArchived
		}
		return nil
	})
}

func titleFromPrompt(prompt string) string {
	if _, body, found := strings.Cut(prompt, envelopeSeparator); found && strings.HasPrefix(prompt, "[agentpack ") {
		prompt = body // a chat asked by another agent is named after the request, not its header
	}
	title := strings.Join(strings.Fields(prompt), " ")
	if runes := []rune(title); len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return title
}

// setTitle names a chat after its first prompt. Titles are product data like names; they never go to diagnostics.
func (worker *WorkerHTTP) setTitle(ctx context.Context, prompt string) {
	title := titleFromPrompt(prompt)
	if title == "" || worker.stateStore == nil {
		return
	}
	if _, err := worker.stateStore.DB.ExecContext(ctx, "UPDATE sessions SET title=? WHERE id=? AND title='' AND archived_at=0 AND node_id="+state.LocalNodeSQL, title, filepath.Base(worker.sessionDir)); err != nil {
		_, event := obs.Start(ctx, "worker", "set_title")
		event.Fail(err)
		event.End()
	}
}
