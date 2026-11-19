package network

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// SubagentSummary is a subagent that is still doing something, shown in the session sidebar.
type SubagentSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	State     string `json:"state"` // running, requires_action or unknown
	StartedAt int64  `json:"started_at"`
}

type subagentState struct {
	title, state string
	startedAt    int64
}

// applySubagent folds subagent_update patches and reports the busy list when it changed, so the
// session row is written on state changes rather than on every update. The caller holds mu.
func (session *ConversationSession) applySubagent(entry acp.LogEntry) ([]SubagentSummary, bool) {
	switch entry.Kind {
	case "agent_exit":
		session.subagents = nil
	case "subagent_update":
		var update struct {
			SessionID string  `json:"sessionId"`
			Title     *string `json:"title"`
			State     *struct {
				State string `json:"state"`
			} `json:"state"`
		}
		if json.Unmarshal(entry.Payload, &update) != nil || update.SessionID == "" {
			return nil, false
		}
		if session.subagents == nil {
			session.subagents = map[string]*subagentState{}
		}
		child := session.subagents[update.SessionID]
		if child == nil {
			child = &subagentState{state: "unknown", startedAt: entry.TS}
			session.subagents[update.SessionID] = child
		}
		if update.Title != nil {
			child.title = *update.Title
		}
		if update.State != nil && update.State.State != "" {
			child.state = update.State.State
		}
	default:
		return nil, false
	}
	var busy []SubagentSummary
	for id, child := range session.subagents {
		if child.state != "idle" {
			busy = append(busy, SubagentSummary{ID: id, Title: child.title, State: child.state, StartedAt: child.startedAt})
		}
	}
	slices.SortFunc(busy, func(a, b SubagentSummary) int {
		return cmp.Or(cmp.Compare(a.StartedAt, b.StartedAt), strings.Compare(a.ID, b.ID))
	})
	saved := ""
	if len(busy) > 0 {
		saved = string(mustRawJSON(busy))
	}
	if saved == session.subagentsSaved {
		return nil, false
	}
	session.subagentsSaved = saved
	return busy, true
}
