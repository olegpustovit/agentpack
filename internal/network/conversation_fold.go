package network

import (
	"encoding/json"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/session"
)

// conversationFold is the one derivation of a chat's state from its log. The log applies it to
// every entry (acp.Fold), and the sink stores it with each batch: the session row's derived
// columns and a checkpoint a restarted worker resumes from.
type conversationFold struct {
	Seq       int64                     `json:"seq"`
	Thread    acp.ThreadReducer         `json:"thread"`
	Subagents map[string]*subagentState `json:"subagents,omitempty"`
	// Goal is the last goal_update, null once cleared. GoalKnown is false while no update was
	// seen, so the row's goal is left as it is.
	Goal      json.RawMessage `json:"goal,omitempty"`
	GoalKnown bool            `json:"goal_known,omitempty"`
	Title     string          `json:"title,omitempty"` // the agent's own name for the chat
}

// conversationDerived is what the session row and the browser's derived frame show.
type conversationDerived struct {
	State     string                    `json:"conversation_state"`
	Working   bool                      `json:"working"`
	NeedsYou  bool                      `json:"needs_you"`
	Waiting   []acp.WaitItem            `json:"waiting,omitempty"`
	Subagents []session.SubagentSummary `json:"subagents,omitempty"`
	// Through is the last sequence folded in. Not "seq": an older relay reads a frame's seq as an entry's.
	Through int64 `json:"through"`
}

func (fold *conversationFold) Apply(entry acp.LogEntry) {
	fold.Seq = entry.Seq
	fold.Thread.Apply(entry)
	fold.applySubagent(entry)
	switch entry.Kind {
	case "goal_update":
		var goal session.Goal
		if json.Unmarshal(entry.Payload, &goal) != nil {
			return
		}
		fold.Goal, fold.GoalKnown = nil, true
		if goal.State != "cleared" {
			fold.Goal = append(json.RawMessage(nil), entry.Payload...)
		}
	case "session_info_update":
		if title := agentTitle(entry); title != "" {
			fold.Title = title
		}
	}
}

func (fold *conversationFold) Checkpoint() json.RawMessage { return mustRawJSON(fold) }

// liveDerived is the session's state now, from its log's fold.
func (cs *ConversationSession) liveDerived() json.RawMessage {
	var derived json.RawMessage
	cs.log.ReadFold(func(fold acp.Fold) {
		if fold, ok := fold.(*conversationFold); ok {
			derived = mustRawJSON(fold.derived())
		}
	})
	return derived
}

func (fold *conversationFold) derived() conversationDerived {
	thread := fold.Thread.State()
	return conversationDerived{State: conversationState(thread), Working: thread.Working, NeedsYou: thread.NeedsYou,
		Waiting: thread.Waiting, Subagents: fold.subagentSummaries(), Through: fold.Seq}
}

// sessionJSON is a derived list as the row stores it: empty for none.
func sessionJSON[T any](items []T) string {
	if len(items) == 0 {
		return ""
	}
	return string(mustRawJSON(items))
}
