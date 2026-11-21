package network

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

type scheduledCall struct {
	delay time.Duration
	run   func()
}

func waitConversationState(t *testing.T, session *ConversationSession, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		got, running := session.state, session.running
		session.mu.Unlock()
		if got == want && (want != "idle" || !running) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", session.ConversationState(), want)
}

func TestUnpromptedUsageSettleClosesAntigravityWakeup(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	scheduled := make(chan scheduledCall, 8)
	session.after = func(delay time.Duration, run func()) { scheduled <- scheduledCall{delay: delay, run: run} }
	session.SetUnpromptedUsageSettle(2 * time.Second)

	// Normal prompted turn: usage_update inside the turn must not schedule an unprompted settle.
	if _, _, err := session.Submit(context.Background(), acp.PromptInput{Text: "deploy"}, false); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- fakeTurn{entries: []acp.LogEntry{
		entry("agent_message_chunk", map[string]any{"text": map[string]string{"text": "Building in background..."}}),
		entry("usage_update", map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 1000}),
	}}
	waitConversationState(t, session, "idle")
	select {
	case call := <-scheduled:
		t.Fatalf("prompted turn scheduled unprompted settle (%v)", call.delay)
	case <-time.After(30 * time.Millisecond):
	}

	// Reactive wakeup starts: thought -> usage_update -> tool_call (should NOT settle while working).
	session.log.Append("tool_call_update", 10, mustRawJSON(map[string]any{"toolCallId": "bg", "status": "completed"}))
	session.log.Append("agent_thought_chunk", 11, mustRawJSON(map[string]any{"text": map[string]string{"text": "Build done."}}))
	waitConversationState(t, session, "working")
	session.log.Append("usage_update", 12, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 120, "size": 1000}))
	thoughtCall := <-scheduled
	if thoughtCall.delay < 15*time.Second {
		t.Fatalf("thought usage settle delay = %v, want >= 15s", thoughtCall.delay)
	}
	session.log.Append("tool_call", 13, mustRawJSON(map[string]any{"toolCallId": "t2", "status": "in_progress"}))
	// Stale timer firing after tool_call arrived must be ignored.
	thoughtCall.run()
	if got := session.ConversationState(); got != "working" {
		t.Fatalf("stale thought timer changed state to %q", got)
	}

	// Commentary + tool_call in the same step: tool_call arriving before settle cancels it.
	session.log.Append("agent_message_chunk", 14, mustRawJSON(map[string]any{"text": map[string]string{"text": "Checking status..."}}))
	session.log.Append("usage_update", 15, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 140, "size": 1000}))
	commentaryCall := <-scheduled
	if commentaryCall.delay != 2*time.Second {
		t.Fatalf("message usage settle delay = %v, want 2s", commentaryCall.delay)
	}
	session.log.Append("tool_call", 16, mustRawJSON(map[string]any{"toolCallId": "t3", "status": "in_progress"}))
	commentaryCall.run()
	if got := session.ConversationState(); got != "working" {
		t.Fatalf("stale commentary timer changed state to %q", got)
	}

	// Final reply -> usage_update, followed only by harmless metadata and subagent events -> settles to idle.
	session.log.Append("tool_call_update", 17, mustRawJSON(map[string]any{"toolCallId": "t3", "status": "completed"}))
	session.log.Append("agent_message_chunk", 18, mustRawJSON(map[string]any{"text": map[string]string{"text": "Done!"}}))
	session.log.Append("usage_update", 19, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 180, "size": 1000}))
	finalCall := <-scheduled
	if finalCall.delay != 2*time.Second {
		t.Fatalf("final usage settle delay = %v, want 2s", finalCall.delay)
	}
	session.log.Append("session_info_update", 20, mustRawJSON(map[string]any{"sessionUpdate": "session_info_update", "title": "Deployed"}))
	session.log.Append("agent_message_chunk", 21, mustRawJSON(map[string]any{"_subagent": "child-1", "text": map[string]string{"text": "ignored"}}))
	finalCall.run()
	waitConversationState(t, session, "idle")

	entries := session.log.Snapshot()
	last := entries[len(entries)-1]
	if last.Kind != "turn_end" {
		t.Fatalf("last entry = %q, want turn_end", last.Kind)
	}
	var payload struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil || payload.StopReason != "end_turn" {
		t.Fatalf("turn_end payload = %s, want end_turn", last.Payload)
	}
}

func TestUnpromptedUsageSettleCancelAndReplay(t *testing.T) {
	// Cancel during an unprompted cycle settles as cancelled when the agent stops replying.
	session, _ := newFakeAgentSession(t)
	scheduled := make(chan scheduledCall, 4)
	session.after = func(delay time.Duration, run func()) { scheduled <- scheduledCall{delay: delay, run: run} }
	session.SetUnpromptedUsageSettle(2 * time.Second)

	session.log.Append("agent_message_chunk", 1, mustRawJSON(map[string]any{"text": map[string]string{"text": "Working..."}}))
	waitConversationState(t, session, "working")
	if err := session.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := <-scheduled
	call.run()
	waitConversationState(t, session, "idle")
	entries := session.log.Snapshot()
	if last := entries[len(entries)-1]; last.Kind != "turn_end" || string(last.Payload) != `{"stopReason":"cancelled"}` {
		t.Fatalf("cancel turn_end = %s %s", last.Kind, last.Payload)
	}

	// Replayed log already ending on an unprompted message -> usage_update settles once configured.
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, mustRawJSON(map[string]any{"text": map[string]string{"text": "go"}}))
	log.Append("turn_end", 2, mustRawJSON(map[string]string{"stopReason": "end_turn"}))
	log.Append("agent_message_chunk", 3, mustRawJSON(map[string]any{"text": map[string]string{"text": "Finished."}}))
	log.Append("usage_update", 4, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 50, "size": 1000}))

	replayed := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	if got := replayed.ConversationState(); got != "working" {
		t.Fatalf("replayed initial state = %q, want working", got)
	}
	replayScheduled := make(chan scheduledCall, 2)
	replayed.after = func(delay time.Duration, run func()) { replayScheduled <- scheduledCall{delay: delay, run: run} }
	replayed.SetUnpromptedUsageSettle(2 * time.Second)
	(<-replayScheduled).run()
	if got := replayed.ConversationState(); got != "idle" {
		t.Fatalf("replayed settled state = %q, want idle", got)
	}
}

func TestUnpromptedUsageSettleDisabledByDefault(t *testing.T) {
	session, _ := newFakeAgentSession(t)
	scheduled := make(chan scheduledCall, 4)
	session.after = func(delay time.Duration, run func()) { scheduled <- scheduledCall{delay: delay, run: run} }

	session.log.Append("agent_message_chunk", 1, mustRawJSON(map[string]any{"text": map[string]string{"text": "Codex step"}}))
	waitConversationState(t, session, "working")
	session.log.Append("usage_update", 2, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 50, "size": 1000}))
	select {
	case call := <-scheduled:
		t.Fatalf("disabled settle scheduled timer (%v)", call.delay)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestUnpromptedUsageSettleRealTimer(t *testing.T) {
	session, _ := newFakeAgentSession(t)
	session.SetUnpromptedUsageSettle(25 * time.Millisecond)

	// First usage_update gets superseded by a tool_call before the real timer fires.
	session.log.Append("agent_message_chunk", 1, mustRawJSON(map[string]any{"text": map[string]string{"text": "Step 1"}}))
	session.log.Append("usage_update", 2, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 1000}))
	session.log.Append("tool_call", 3, mustRawJSON(map[string]any{"toolCallId": "t1", "status": "in_progress"}))
	time.Sleep(50 * time.Millisecond)
	if got := session.ConversationState(); got != "working" {
		t.Fatalf("superseded real timer changed state to %q", got)
	}

	// Quiet message -> usage_update settles via real time.AfterFunc.
	session.log.Append("tool_call_update", 4, mustRawJSON(map[string]any{"toolCallId": "t1", "status": "completed"}))
	session.log.Append("agent_message_chunk", 5, mustRawJSON(map[string]any{"text": map[string]string{"text": "All done"}}))
	session.log.Append("usage_update", 6, mustRawJSON(map[string]any{"sessionUpdate": "usage_update", "used": 20, "size": 1000}))
	waitConversationState(t, session, "idle")
}
