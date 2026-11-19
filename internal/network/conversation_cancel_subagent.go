package network

import (
	"context"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// CancelSubagent stops one announced subagent session; the parent turn keeps running.
func (session *ConversationSession) CancelSubagent(ctx context.Context, id string) error {
	session.mu.Lock()
	broker, log := session.broker, session.log
	session.mu.Unlock()
	return acp.CancelSubagent(ctx, log, broker, id)
}
