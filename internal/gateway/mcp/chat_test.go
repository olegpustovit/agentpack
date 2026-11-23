package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
)

// helperFixture records a running chat with parent, as a start does.
func helperFixture(t *testing.T, m *Server, id, parent string) {
	t.Helper()
	owner := session.BrowserActor("owner@example.com")
	if err := session.UpsertSession(t.Context(), m.store.DB, session.SessionInfo{ID: id, Harness: "claude", Transport: "acp", Tools: true, ParentID: parent,
		StartedBy: &owner, ConversationState: "idle", State: session.TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestHelperDepthAndFanOutCaps(t *testing.T) {
	m := testServer(t)
	helperFixture(t, m, "session-root", "")
	helperFixture(t, m, "session-one", "session-root")
	helperFixture(t, m, "session-two", "session-one")
	if err := m.helperAllowed(t.Context(), "session-two"); err == nil || !strings.Contains(err.Error(), "levels deep") {
		t.Fatal("third level allowed:", err)
	}
	if err := m.helperAllowed(t.Context(), "session-one"); err != nil {
		t.Fatal("second level refused:", err)
	}
	for _, id := range []string{"session-a", "session-b", "session-c", "session-d"} {
		helperFixture(t, m, id, "session-root")
	}
	if _, err := m.store.DB.Exec("UPDATE sessions SET conversation_state='working'"); err != nil {
		t.Fatal(err)
	}
	if err := m.helperAllowed(t.Context(), "session-one"); err == nil || !strings.Contains(err.Error(), "6 helpers") {
		t.Fatal("seventh helper allowed:", err)
	}
	if err := session.EndSession(t.Context(), m.store.DB, "session-a", session.TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := m.helperAllowed(t.Context(), "session-root"); err != nil {
		t.Fatal("a finished helper still counts:", err)
	}
	helperFixture(t, m, "session-e", "session-root")
	if _, err := m.store.DB.Exec("UPDATE sessions SET conversation_state='working'"); err != nil {
		t.Fatal(err)
	}
	if err := m.helperAllowed(t.Context(), "session-root"); err == nil {
		t.Fatal("seventh working helper allowed")
	}
	// A helper whose task is done waits idle; it does not hold a slot.
	if _, err := m.store.DB.Exec("UPDATE sessions SET conversation_state='idle' WHERE id='session-b'"); err != nil {
		t.Fatal(err)
	}
	if err := m.helperAllowed(t.Context(), "session-root"); err != nil {
		t.Fatal("an idle helper still counts:", err)
	}
	if err := m.helperAllowed(t.Context(), "session-missing"); err == nil {
		t.Fatal("unknown parent allowed")
	}
}

func TestWaitConditions(t *testing.T) {
	first := map[string]network.ChatState{"a": network.StateWorking, "b": network.StateIdle}
	for _, test := range []struct {
		until waitUntil
		views []mcpSession
		want  bool
	}{
		{untilNeedsYou, []mcpSession{{ID: "a", State: network.StateWorking}}, false},
		{untilNeedsYou, []mcpSession{{ID: "a", State: network.StateNeedsYou}}, true},
		{untilIdle, []mcpSession{{ID: "a", State: network.StateStopped}}, true},
		{untilAny, []mcpSession{{ID: "a", State: network.StateWorking}, {ID: "b", State: network.StateIdle}}, false},
		{untilAny, []mcpSession{{ID: "a", State: network.StateWorking}, {ID: "b", State: network.StateWorking}}, true},
	} {
		if got := reached(test.views, first, test.until); got != test.want {
			t.Fatalf("%s %+v = %v", test.until, test.views, got)
		}
	}
	if summary := attentionSummary([]mcpAttention{{mcpSession: mcpSession{Agent: "claude", Directory: "/w/agentpack"}, Waiting: []network.McpTranscriptItem{{Kind: "permission", Text: "Run go test"}}}}); summary != "One session needs you. Claude Code in agentpack asks permission: Run go test." {
		t.Fatal(summary)
	}
}

func TestHelperReplyEndsAtTheFirstTurn(t *testing.T) {
	entries := []acp.LogEntry{
		{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"Find the bug"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"Looking"}}`)},
		{Seq: 3, Kind: "tool_call", Payload: json.RawMessage(`{"toolCallId":"t1","title":"grep"}`)},
		{Seq: 4, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"_subagent":"c1","text":{"text":"child"}}`)},
		{Seq: 5, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"It is "}}`)},
		{Seq: 6, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"line 4."}}`)},
	}
	if done, reply := helperReply(entries); done || reply != "Looking\n\nIt is line 4." {
		t.Fatalf("running: done=%v reply=%q", done, reply)
	}
	entries = append(entries,
		acp.LogEntry{Seq: 7, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)},
		acp.LogEntry{Seq: 8, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"next turn"}}`)},
	)
	if done, reply := helperReply(entries); !done || reply != "Looking\n\nIt is line 4." {
		t.Fatalf("ended: done=%v reply=%q", done, reply)
	}
}
