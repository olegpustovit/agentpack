package network

import (
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func foldScript() []acp.LogEntry {
	raw := func(kind string, payload any) acp.LogEntry {
		return acp.LogEntry{Kind: kind, TS: 1000, Payload: mustRawJSON(payload)}
	}
	return []acp.LogEntry{
		raw("prompt", map[string]any{"text": map[string]string{"text": "ship it"}}),
		raw("agent_message_chunk", map[string]any{"text": map[string]string{"text": "on "}}),
		raw("agent_message_chunk", map[string]any{"text": map[string]string{"text": "it"}}),
		raw("goal_update", Goal{Objective: "ship", State: "active", Engine: "gateway"}),
		raw("session_info_update", map[string]any{"sessionUpdate": "session_info_update", "title": "Shipping"}),
		raw("subagent_update", map[string]any{"sessionId": "child", "title": "Helper", "state": map[string]string{"state": "running"}}),
		raw("tool_call", map[string]any{"toolCallId": "w", "title": "ScheduleWakeup", "rawInput": map[string]any{"delaySeconds": 60}}),
		raw("permission_request", map[string]any{"requestId": "r1", "toolCall": map[string]string{"toolCallId": "t"}}),
		raw("permission_result", map[string]any{"requestId": "r1", "optionId": "allow"}),
		raw("turn_end", map[string]string{"stopReason": "end_turn"}),
		raw("permission_request", map[string]any{"requestId": "r2", "toolCall": map[string]string{"toolCallId": "t2"}}),
		raw("subagent_update", map[string]any{"sessionId": "child", "state": map[string]string{"state": "idle"}}),
		raw("goal_update", Goal{Objective: "ship", State: "cleared", Engine: "gateway"}),
		raw("agent_exit", map[string]int{"code": 0}),
	}
}

type derivedRow struct {
	State, Waiting, Subagents, Goal, Title string
	LastSeq, CheckpointSeq                 int64
}

// P3.1: at every commit the session row is the fold of the stored rows up to last_seq.
func TestSessionRowIsTheFoldOfStoredEventsAtEveryCommit(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-fold"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log, err := newConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i, entry := range foldScript() {
		log.Append(entry.Kind, entry.TS, entry.Payload)
		if i%3 == 1 { // commits cover single entries and batches
			continue
		}
		if err := log.Flush(); err != nil {
			t.Fatal(err)
		}
		var row derivedRow
		if err := store.Reader.QueryRow(`SELECT s.conversation_state,s.waiting,s.subagents,s.goal,s.title,s.last_seq,c.seq
			FROM sessions s JOIN conversation_checkpoints c ON c.session_id=s.id WHERE s.id=?`, id).
			Scan(&row.State, &row.Waiting, &row.Subagents, &row.Goal, &row.Title, &row.LastSeq, &row.CheckpointSeq); err != nil {
			t.Fatal(err)
		}
		stored, lastSeq, _, err := storedConversationTail(t.Context(), store.Reader, id, 0)
		if err != nil {
			t.Fatal(err)
		}
		fold := &conversationFold{}
		for _, entry := range stored {
			fold.Apply(entry)
		}
		derived := fold.derived()
		want := derivedRow{State: derived.State, Waiting: sessionJSON(derived.Waiting), Subagents: sessionJSON(derived.Subagents),
			Goal: string(fold.Goal), Title: fold.Title, LastSeq: lastSeq, CheckpointSeq: lastSeq}
		if row != want {
			t.Fatalf("after entry %d (%s) row = %+v\nfold = %+v", i+1, entry.Kind, row, want)
		}
	}
}

// P3.3: a restarted worker resumes the fold from its checkpoint, not from the rows it can still read.
func TestConversationFoldRestoresFromCheckpoint(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-restore"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp"}); err != nil {
		t.Fatal(err)
	}
	log, err := newConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	script := foldScript()[:11] // ends on an open permission request, a pending wake-up and a subagent
	for _, entry := range script {
		log.Append(entry.Kind, entry.TS, entry.Payload)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	saved := json.RawMessage(log.Fold().Checkpoint())
	// Rows before the last one are gone, as if they had fallen out of the replay tail.
	if _, err := store.DB.Exec("DELETE FROM conversation_events WHERE session_id=? AND seq<?", id, len(script)); err != nil {
		t.Fatal(err)
	}
	resumed, err := newConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Fold().Checkpoint(); string(got) != string(saved) {
		t.Fatalf("restored fold\n%s\nwant\n%s", got, saved)
	}
	session := NewConversationSession(resumed, nil)
	if session.ConversationState() != string(stateNeedsYou) {
		t.Fatalf("state = %s, want the open request from before the tail", session.ConversationState())
	}
	thread := session.fold.Thread.State()
	if len(thread.Waiting) != 1 || thread.Waiting[0].Kind != "wake" || len(session.fold.subagentSummaries()) != 1 {
		t.Fatalf("restored thread = %+v", thread)
	}
	// The restored reducer keeps working: answering the request clears it.
	resumed.Append("permission_result", 2000, mustRawJSON(map[string]string{"requestId": "r2", "optionId": "allow"}))
	if session.ConversationState() != string(stateIdle) {
		t.Fatalf("state after answer = %s", session.ConversationState())
	}
	var reducer acp.ThreadReducer
	if err := json.Unmarshal(mustRawJSON(&session.fold.Thread), &reducer); err != nil || !reflect.DeepEqual(reducer.State(), session.fold.Thread.State()) {
		t.Fatalf("reducer checkpoint does not round-trip: %v", err)
	}
}

// An instant start's row stays "starting" while its worker holds it, then shows the fold.
func TestStartingRowHoldsUntilRepublished(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	id := "session-instant"
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", ConversationState: "starting"}); err != nil {
		t.Fatal(err)
	}
	var starting atomic.Bool
	starting.Store(true)
	log, err := newConversationLog(t.Context(), store, id, func(err error) { t.Error(err) }, &starting)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	NewConversationSession(log, nil)
	state := func() string {
		info, err := storedSession(t.Context(), store.Reader, id)
		if err != nil {
			t.Fatal(err)
		}
		return info.ConversationState
	}
	log.Append("permission_request", 1, json.RawMessage(`{"requestId":"p1"}`))
	if got := state(); got != "starting" {
		t.Fatalf("held state = %q", got)
	}
	if err := upsertSession(t.Context(), store.DB, SessionInfo{ID: id, Transport: "acp", ConversationState: "idle"}); err != nil {
		t.Fatal(err)
	}
	starting.Store(false)
	if err := log.Republish(); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "needs_you" {
		t.Fatalf("state after start = %q; an upsert must not overwrite the fold", got)
	}
}
