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
	Title     string `json:"title,omitempty"`
	State     string `json:"state"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	EndedSeq  int64  `json:"ended_seq,omitempty"` // orders finishes within one millisecond
}

// applySubagent folds subagent_update patches; an agent exit forgets every subagent.
func (fold *conversationFold) applySubagent(entry acp.LogEntry) {
	switch entry.Kind {
	case "agent_exit":
		fold.Subagents = nil
	case "subagent_update":
		var update struct {
			SessionID string  `json:"sessionId"`
			Title     *string `json:"title"`
			State     *struct {
				State string `json:"state"`
			} `json:"state"`
		}
		if json.Unmarshal(entry.Payload, &update) != nil || update.SessionID == "" {
			return
		}
		if fold.Subagents == nil {
			fold.Subagents = map[string]*subagentState{}
		}
		child := fold.Subagents[update.SessionID]
		if child == nil {
			child = &subagentState{State: "unknown", StartedAt: entry.TS}
			fold.Subagents[update.SessionID] = child
		}
		if update.Title != nil {
			child.Title = *update.Title
		}
		if update.State != nil && update.State.State != "" {
			if update.State.State == "idle" && child.State != "idle" {
				child.EndedAt, child.EndedSeq = entry.TS, entry.Seq
			}
			child.State = update.State.State
		}
	}
}

// subagentSummaries lists the busy subagents and the latest finished ones, oldest first.
func (fold *conversationFold) subagentSummaries() []SubagentSummary {
	var busy []SubagentSummary
	var finished []*subagentState
	finishedIDs := map[*subagentState]string{}
	for id, child := range fold.Subagents {
		if child.State == "idle" {
			finished = append(finished, child)
			finishedIDs[child] = id
		} else {
			busy = append(busy, SubagentSummary{ID: id, Title: child.Title, State: child.State, StartedAt: child.StartedAt})
		}
	}
	slices.SortFunc(finished, func(a, b *subagentState) int { return cmp.Compare(b.EndedSeq, a.EndedSeq) })
	for _, child := range finished[:min(len(finished), finishedSubagentsShown)] {
		busy = append(busy, SubagentSummary{ID: finishedIDs[child], Title: child.Title, State: child.State, StartedAt: child.StartedAt, EndedAt: child.EndedAt})
	}
	slices.SortFunc(busy, func(a, b SubagentSummary) int {
		return cmp.Or(cmp.Compare(a.StartedAt, b.StartedAt), strings.Compare(a.ID, b.ID))
	})
	return busy
}
