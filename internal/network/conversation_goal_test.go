package network

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func startGatewayGoal(t *testing.T, limits *GoalLimits) (*ConversationSession, *fakeAgent) {
	t.Helper()
	session, agent := newFakeAgentSession(t)
	if err := session.SetGoal(t.Context(), "ship it", "tests pass", limits); err != nil {
		t.Fatal(err)
	}
	if goal := agent.goalUntil(t, goalIn("active")); goal.Engine != "gateway" {
		t.Fatalf("engine = %s", goal.Engine)
	}
	return session, agent
}

// runGoalTurns answers each continuation with turn until the goal leaves the active state.
func runGoalTurns(t *testing.T, agent *fakeAgent, turn fakeTurn) *Goal {
	t.Helper()
	for range 10 {
		prompt := agent.nextPrompt(t)
		if !strings.Contains(prompt.Text, "Goal: ship it") || !strings.Contains(prompt.Text, "Done when: tests pass") {
			t.Fatalf("continuation = %q", prompt.Text)
		}
		agent.turns <- turn
		goal := agent.goalUntil(t, func(*Goal) bool { return true })
		if goal.State != "active" {
			return goal
		}
	}
	t.Fatal("the goal never stopped")
	return nil
}

func TestGatewayGoalStopRules(t *testing.T) {
	usage := func(used int64, cost float64) acp.LogEntry {
		return entry("usage_update", map[string]any{"used": used, "size": 1000, "cost": map[string]any{"amount": cost, "currency": "USD"}})
	}
	withUsage := func(turn fakeTurn, update acp.LogEntry) fakeTurn {
		turn.entries = append(turn.entries, update)
		return turn
	}
	slow := reply("GOAL: continue", true)
	slow.advance = 61 * time.Second
	for _, test := range []struct {
		name   string
		limits *GoalLimits
		turn   fakeTurn
		state  string
		reason string
		turns  int
	}{
		{name: "met", turn: reply("All green.\n**GOAL: met** suite passes", true), state: "met", reason: "suite passes", turns: 1},
		{name: "blocked three times", turn: reply("GOAL: blocked need an API key", true), state: "blocked", reason: "need an API key", turns: 3},
		{name: "no progress", turn: reply("thinking\nGOAL: continue", false), state: "blocked", reason: "no progress", turns: 3},
		{name: "turn limit", limits: &GoalLimits{Turns: 2}, turn: reply("GOAL: continue", true), state: "limited", reason: "turn limit reached", turns: 2},
		{name: "time limit", limits: &GoalLimits{Minutes: 1}, turn: slow, state: "limited", reason: "time limit reached", turns: 1},
		{name: "token limit", limits: &GoalLimits{Tokens: 100}, turn: withUsage(reply("GOAL: continue", true), usage(150, 0)), state: "limited", reason: "token limit reached", turns: 1},
		{name: "cost limit", limits: &GoalLimits{CostUSD: 1}, turn: withUsage(reply("GOAL: continue", true), usage(10, 2)), state: "limited", reason: "cost limit reached", turns: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, agent := startGatewayGoal(t, test.limits)
			goal := runGoalTurns(t, agent, test.turn)
			if goal.State != test.state || goal.LastReason != test.reason || goal.Turns != test.turns || goal.ResumedAt != 0 {
				t.Fatalf("goal = %+v", goal)
			}
		})
	}
}

func TestUserStopPausesGoal(t *testing.T) {
	session, agent := startGatewayGoal(t, nil)
	agent.nextPrompt(t)
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-agent.cancels
	agent.turns <- fakeTurn{stop: "cancelled"}
	if goal := agent.goalUntil(t, goalIn("paused")); goal.LastReason != "stopped by the user" || goal.Turns != 1 {
		t.Fatalf("goal = %+v", goal)
	}
}

func TestGoalPauseResumeKeepsActiveTime(t *testing.T) {
	session, agent := startGatewayGoal(t, nil)
	agent.nextPrompt(t)
	agent.mu.Lock()
	agent.now = agent.now.Add(5 * time.Second)
	agent.mu.Unlock()
	if err := session.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if goal := agent.goalUntil(t, goalIn("paused")); goal.ActiveMS != 5000 || goal.ResumedAt != 0 {
		t.Fatalf("paused goal = %+v", goal)
	}
	agent.turns <- fakeTurn{}
	agent.mu.Lock()
	agent.now = agent.now.Add(time.Minute) // paused time never counts
	agent.mu.Unlock()
	if err := session.UpdateGoal(t.Context(), GoalChange{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	goal := agent.goalUntil(t, goalIn("active"))
	if goal.ActiveMS != 5000 || goal.ResumedAt != 1_000_000+5_000+60_000 {
		t.Fatalf("resumed goal = %+v", goal)
	}
	agent.nextPrompt(t) // the worker continues the goal
	agent.turns <- reply("GOAL: met", true)
	agent.goalUntil(t, goalIn("met"))
}

func TestNativeGoalUsesAgentCommandAndSnapshots(t *testing.T) {
	session, agent := newFakeAgentSession(t, entry("available_commands_update", map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "goal", "description": "goal"}},
	}))
	if err := session.SetGoal(t.Context(), "ship it", "tests pass", nil); err != nil {
		t.Fatal(err)
	}
	if goal := agent.goalUntil(t, goalIn("active")); goal.Engine != "native" {
		t.Fatalf("goal = %+v", goal)
	}
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal ship it. Done when: tests pass" {
		t.Fatalf("native command = %q", prompt.Text)
	}
	snapshot := func(goal any) acp.LogEntry {
		return entry("session_info_update", map[string]any{"sessionUpdate": "session_info_update",
			"_meta": map[string]any{"jetbrains": map[string]any{"air": map[string]any{"goal": goal}}}})
	}
	agent.turns <- fakeTurn{entries: []acp.LogEntry{snapshot(map[string]any{"objective": "ship it", "status": "complete", "iterations": 4, "lastReason": "done"})}}
	if goal := agent.goalUntil(t, goalIn("met")); goal.Turns != 4 || goal.LastReason != "done" {
		t.Fatalf("goal = %+v", goal)
	}
	agent.log.Append("session_info_update", 2, snapshot(nil).Payload)
	agent.goalUntil(t, func(goal *Goal) bool { return goal == nil })
}

func TestSubagentSummaryTracksBusyChildren(t *testing.T) {
	session, _ := newFakeAgentSession(t)
	saved := make(chan []SubagentSummary, 8)
	session.subagentsChange = func(list []SubagentSummary) { saved <- list }
	update := func(id string, fields map[string]any) {
		fields["sessionUpdate"], fields["sessionId"] = "subagent_update", id
		session.log.Append("subagent_update", 5, mustRawJSON(fields))
	}
	update("child", map[string]any{"title": "Search", "state": map[string]string{"state": "running"}})
	if list := <-saved; len(list) != 1 || list[0].Title != "Search" || list[0].State != "running" || list[0].StartedAt != 5 {
		t.Fatalf("list = %+v", list)
	}
	update("child", map[string]any{"description": "same state"})
	update("child", map[string]any{"state": map[string]string{"state": "idle"}})
	if list := <-saved; len(list) != 0 {
		t.Fatalf("an unchanged update was saved, or idle child kept: %+v", list)
	}
	update("other", map[string]any{"state": map[string]string{"state": "requires_action"}})
	<-saved
	session.log.Append("agent_exit", 6, []byte(`{}`))
	if list := <-saved; list != nil {
		t.Fatalf("exit kept %+v", list)
	}
}

func TestGoalAndSubagentsPersistOnSessionRow(t *testing.T) {
	store := testSessionStore(t, t.TempDir())
	info := SessionInfo{ID: "session-goal", Directory: t.TempDir(), Harness: "codex", Transport: "acp"}
	if err := upsertSession(t.Context(), store.DB, info); err != nil {
		t.Fatal(err)
	}
	goal := &Goal{Objective: "ship", State: "active", Engine: "gateway", StartedAt: 1, ResumedAt: 1, UpdatedAt: 1}
	persistSessionJSON(t.Context(), store.DB, info.ID, "goal", goal, false)
	persistSessionJSON(t.Context(), store.DB, info.ID, "subagents", []SubagentSummary{{ID: "c", State: "running"}}, false)
	if err := upsertSession(t.Context(), store.DB, info); err != nil { // an upsert must not clobber worker-owned columns
		t.Fatal(err)
	}
	stored, err := storedSession(t.Context(), store.Reader, info.ID)
	if err != nil || stored.Goal == nil || stored.Goal.Objective != "ship" || len(stored.Subagents) != 1 {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	persistSessionJSON(t.Context(), store.DB, info.ID, "goal", (*Goal)(nil), true)
	if stored, _ = storedSession(t.Context(), store.Reader, info.ID); stored.Goal != nil {
		t.Fatalf("cleared goal = %+v", stored.Goal)
	}
}

// Codex reports no goal status to a plain ACP client: after the /goal turn it keeps working with no
// prompt and only its thread status says when it stops.
func TestNativeGoalFollowsUnpromptedWork(t *testing.T) {
	session, agent := newFakeAgentSession(t, entry("available_commands_update", map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "goal", "description": "goal"}},
	}))
	thread := func(status string) {
		agent.log.Append("session_info_update", 2, mustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
			"_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": status}}}}))
	}
	if err := session.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- fakeTurn{}
	agent.goalUntil(t, goalIn("met")) // Claude's /goal ends here
	thread("active")
	agent.goalUntil(t, goalIn("active"))
	thread("idle")
	goal := agent.goalUntil(t, goalIn("met"))
	session.mu.Lock()
	state := session.state
	session.mu.Unlock()
	if state != "idle" || goal.LastReason != "the agent finished" {
		t.Fatalf("state %q goal %+v", state, goal)
	}
	agent.log.Append("prompt", 3, mustRawJSON(map[string]any{"text": map[string]string{"text": "thanks"}}))
	session.mu.Lock()
	revived := session.goal.State != "met"
	session.mu.Unlock()
	if revived {
		t.Fatal("a user prompt revived a met goal")
	}
}

func TestStopDuringUnpromptedNativeGoalPauses(t *testing.T) {
	session, agent := newFakeAgentSession(t, entry("available_commands_update", map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "goal", "description": "goal"}},
	}))
	if err := session.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- fakeTurn{}
	agent.goalUntil(t, goalIn("met"))
	agent.log.Append("agent_message_chunk", 2, mustRawJSON(map[string]any{"text": map[string]string{"text": "still going"}}))
	agent.goalUntil(t, goalIn("active"))
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	agent.log.Append("session_info_update", 3, mustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": "idle"}}}}))
	agent.goalUntil(t, goalIn("paused"))
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal pause" {
		t.Fatalf("prompt = %q", prompt.Text)
	}
}

// A long chat's goal update can lie before the replayed tail; the saved row restores it, so the
// person can still clear it.
func TestGoalBeyondTheReplayedTailIsRestoredAndClears(t *testing.T) {
	session, agent := newFakeAgentSession(t)
	session.restoreGoal(&Goal{Objective: "ship it", State: "active", Engine: "native"})
	if session.goal == nil || session.goal.Objective != "ship it" {
		t.Fatalf("goal = %+v", session.goal)
	}
	session.restoreGoal(&Goal{Objective: "older", State: "active"}) // the transcript's own goal wins
	if session.goal.Objective != "ship it" {
		t.Fatalf("goal replaced: %+v", session.goal)
	}
	if err := session.ClearGoal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if input := agent.nextPrompt(t); input.Text != "/goal clear" {
		t.Fatalf("sent %q", input.Text)
	}
	agent.turns <- fakeTurn{}
}
