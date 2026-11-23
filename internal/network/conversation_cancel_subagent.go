package network

import (
	"context"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// CancelSubagent stops one announced subagent session; the parent turn keeps running.
func (cs *ConversationSession) CancelSubagent(ctx context.Context, id string) error {
	cs.mu.Lock()
	broker, log := cs.broker, cs.Log
	cs.mu.Unlock()
	return acp.CancelSubagent(ctx, log, broker, id)
}
