package network

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// SubagentSummary is a subagent shown under its chat in the session sidebar: every one that is
// still doing something, and the most recent that finished.
type SubagentSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	State     string `json:"state"` // running, requires_action, unknown or idle (finished)
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
}

// finishedSubagentsShown bounds the finished subagents a chat lists, so a long goal stays short.
const finishedSubagentsShown = 5

type subagentState struct {
	title, state       string
	startedAt, endedAt int64
	endedSeq           int64 // orders finishes within one millisecond
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
			if update.State.State == "idle" && child.state != "idle" {
				child.endedAt, child.endedSeq = entry.TS, entry.Seq
			}
			child.state = update.State.State
		}
	default:
		return nil, false
	}
	var busy []SubagentSummary
	var finished []*subagentState
	finishedIDs := map[*subagentState]string{}
	for id, child := range session.subagents {
		if child.state == "idle" {
			finished = append(finished, child)
			finishedIDs[child] = id
		} else {
			busy = append(busy, SubagentSummary{ID: id, Title: child.title, State: child.state, StartedAt: child.startedAt})
		}
	}
	slices.SortFunc(finished, func(a, b *subagentState) int { return cmp.Compare(b.endedSeq, a.endedSeq) })
	for _, child := range finished[:min(len(finished), finishedSubagentsShown)] {
		busy = append(busy, SubagentSummary{ID: finishedIDs[child], Title: child.title, State: child.state, StartedAt: child.startedAt, EndedAt: child.endedAt})
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
