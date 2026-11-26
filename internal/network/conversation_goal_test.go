package network

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/session"
)

func startGatewayGoal(t *testing.T, limits *session.GoalLimits) (*ConversationSession, *fakeAgent) {
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
func runGoalTurns(t *testing.T, agent *fakeAgent, turn fakeTurn) *session.Goal {
	t.Helper()
	for range 10 {
		prompt := agent.nextPrompt(t)
		if !strings.Contains(prompt.Text, "Goal: ship it") || !strings.Contains(prompt.Text, "Done when: tests pass") {
			t.Fatalf("continuation = %q", prompt.Text)
		}
		agent.turns <- turn
		goal := agent.goalUntil(t, func(*session.Goal) bool { return true })
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
		limits *session.GoalLimits
		turn   fakeTurn
		state  string
		reason string
		turns  int
	}{
		{name: "met", turn: reply("All green.\n**GOAL: met** suite passes", true), state: "met", reason: "suite passes", turns: 1},
		{name: "blocked three times", turn: reply("GOAL: blocked need an API key", true), state: "blocked", reason: "need an API key", turns: 3},
		{name: "no progress", turn: reply("thinking\nGOAL: continue", false), state: "blocked", reason: "no progress", turns: 3},
		{name: "turn limit", limits: &session.GoalLimits{Turns: 2}, turn: reply("GOAL: continue", true), state: "limited", reason: "turn limit reached", turns: 2},
		{name: "time limit", limits: &session.GoalLimits{Minutes: 1}, turn: slow, state: "limited", reason: "time limit reached", turns: 1},
		{name: "token limit", limits: &session.GoalLimits{Tokens: 100}, turn: withUsage(reply("GOAL: continue", true), usage(150, 0)), state: "limited", reason: "token limit reached", turns: 1},
		{name: "cost limit", limits: &session.GoalLimits{CostUSD: 1}, turn: withUsage(reply("GOAL: continue", true), usage(10, 2)), state: "limited", reason: "cost limit reached", turns: 1},
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
	conv, agent := newFakeAgentSession(t, entry("available_commands_update", map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "goal", "description": "goal"}},
	}))
	if err := conv.SetGoal(t.Context(), "ship it", "tests pass", nil); err != nil {
		t.Fatal(err)
	}
	if goal := agent.goalUntil(t, goalIn("active")); goal.Engine != "native" {
		t.Fatalf("goal = %+v", goal)
	}
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal ship it. Done when: tests pass."+nativeGoalDeferral {
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
	agent.goalUntil(t, noGoal)
}

// Claude's background Agent tasks arrive as AIR's subagent_spawned / subagent_state_update; the
// session tree lists them while they run and the most recent once they finish.
func TestSubagentSummaryTracksBackgroundAgents(t *testing.T) {
	conv, _ := newFakeAgentSession(t)
	// The fold is applied as each update is appended, so its summary is current after every call.
	summary := func() []session.SubagentSummary { return conv.fold.subagentSummaries() }
	air := func(update string) {
		line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"parent","update":` + update + `}}`
		if !acp.AppendRawSessionUpdate(conv.Log, []byte(line)) {
			t.Fatalf("not consumed: %s", update)
		}
	}
	spawn := func(id string) {
		air(`{"sessionUpdate":"subagent_spawned","subagentSessionId":"` + id + `","name":"Worker ` + id + `","task":"t","capabilities":{}}`)
	}
	spawn("a")
	if list := summary(); len(list) != 1 || list[0].Title != "Worker a" || list[0].State != "running" {
		t.Fatalf("running = %+v", list)
	}
	air(`{"sessionUpdate":"subagent_state_update","subagentSessionId":"a","state":"completed"}`)
	if list := summary(); len(list) != 1 || list[0].State != "idle" || list[0].EndedAt == 0 {
		t.Fatalf("a finished background agent stays in the tree: %+v", list)
	}
	for _, id := range []string{"b", "c", "d", "e", "f", "g"} {
		spawn(id)
		air(`{"sessionUpdate":"subagent_state_update","subagentSessionId":"` + id + `","state":"completed"}`)
	}
	spawn("h")
	list := summary()
	if len(list) != finishedSubagentsShown+1 || list[len(list)-1].ID != "h" || slices.ContainsFunc(list, func(s session.SubagentSummary) bool { return s.ID == "a" }) {
		t.Fatalf("tree = %+v; want the running one and the %d most recent finished", list, finishedSubagentsShown)
	}
	conv.Log.Append("agent_exit", 6, []byte(`{}`))
	if list := summary(); list != nil {
		t.Fatalf("exit kept %+v", list)
	}
}

// An adapter without goal status can continue unprompted work. Its thread status drives the
// legacy goal inference.
func TestLegacyNativeGoalFollowsUnpromptedWork(t *testing.T) {
	session, agent := newFakeAgentSession(t, entry("available_commands_update", map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "goal", "description": "goal"}},
	}))
	thread := func(status string) {
		agent.log.Append("session_info_update", 2, MustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
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
	agent.log.Append("prompt", 3, MustRawJSON(map[string]any{"text": map[string]string{"text": "thanks"}}))
	agent.goalUntil(t, noGoal)
}

// A met goal leaves when the person's next prompt starts: not on an agent's message or a goal
// command, not while history replays, and a repeated completion does not bring it back.
func TestPersonPromptRetiresAMetGoal(t *testing.T) {
	complete := claudeGoalCheck(map[string]any{"objective": "ship it. Done when: tests pass", "status": "complete"})
	prompt := func(input acp.PromptInput) acp.LogEntry {
		return acp.LogEntry{Kind: "prompt", TS: 1, Payload: PromptLogPayload(input)}
	}
	// A chat from before this rule: the prompt after the met goal logged no clear.
	conv, agent := newFakeAgentSession(t,
		entry("goal_update", session.Goal{Objective: "ship it", State: "met", Engine: "native"}),
		prompt(acp.PromptInput{Text: "thanks"}))
	conv.publishGoal()
	if kinds := strings.Join(logKinds(agent.log), ","); kinds != "goal_update,prompt" || goalNow(conv).State != "met" {
		t.Fatalf("replay published %s, goal %+v", kinds, goalNow(conv))
	}
	for _, input := range []acp.PromptInput{messageFromAgent("a1", "question"), {Text: "/goal clear it"}} {
		agent.log.Append("prompt", 2, PromptLogPayload(input))
		if goalNow(conv).State != "met" {
			t.Fatalf("%q retired the goal", input.Text)
		}
	}
	agent.log.Append("prompt", 3, PromptLogPayload(acp.PromptInput{Text: "next thing"}))
	agent.goalUntil(t, noGoal)
	agent.log.Append("session_info_update", 4, complete)
	conv.mu.Lock()
	defer conv.mu.Unlock()
	if conv.goal != nil || conv.goalDirty {
		t.Fatalf("a repeated completion recreated the goal: %+v", conv.goal)
	}
}

func TestStopDuringUnpromptedNativeGoalPauses(t *testing.T) {
	session, agent := newFakeAgentSession(t, codexGoalCommand)
	if err := session.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- fakeTurn{}
	agent.goalUntil(t, goalIn("met"))
	agent.log.Append("agent_message_chunk", 2, MustRawJSON(map[string]any{"text": map[string]string{"text": "still going"}}))
	agent.goalUntil(t, goalIn("active"))
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	agent.log.Append("session_info_update", 3, MustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": "idle"}}}}))
	agent.goalUntil(t, goalIn("paused"))
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal pause" {
		t.Fatalf("prompt = %q", prompt.Text)
	}
}

// A long chat's goal update can lie before the replayed tail; the saved row restores it, so the
// person can still clear it.
func TestGoalBeyondTheReplayedTailIsRestoredAndClears(t *testing.T) {
	conv, agent := newFakeAgentSession(t)
	conv.restoreGoal(&session.Goal{Objective: "ship it", State: "active", Engine: "native"})
	if conv.goal == nil || conv.goal.Objective != "ship it" {
		t.Fatalf("goal = %+v", conv.goal)
	}
	conv.restoreGoal(&session.Goal{Objective: "older", State: "active"}) // the transcript's own goal wins
	if conv.goal.Objective != "ship it" {
		t.Fatalf("goal replaced: %+v", conv.goal)
	}
	if err := conv.ClearGoal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if input := agent.nextPrompt(t); input.Text != "/goal clear" {
		t.Fatalf("sent %q", input.Text)
	}
	agent.turns <- fakeTurn{}
}

// A stop that lands while the turn loop is between turns pauses the native goal when the loop
// lets the agent go; it has no turn left whose end would send the pause.
func TestStopBetweenTurnsPausesNativeGoalOnRelease(t *testing.T) {
	session, agent := newFakeAgentSession(t, codexGoalCommand)
	if err := session.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	agent.turns <- fakeTurn{}
	agent.goalUntil(t, goalIn("met"))
	agent.log.Append("agent_message_chunk", 2, MustRawJSON(map[string]any{"text": map[string]string{"text": "still going"}}))
	agent.goalUntil(t, goalIn("active"))
	session.mu.Lock()
	for session.running { // wait for the first loop to let go, then pose as one between turns
		session.mu.Unlock()
		runtime.Gosched()
		session.mu.Lock()
	}
	session.running = true
	session.mu.Unlock()
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	agent.log.Append("session_info_update", 3, MustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": "idle"}}}}))
	agent.goalUntil(t, goalIn("paused"))
	go session.runTurns(context.Background(), nil, nil)
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal pause" {
		t.Fatalf("prompt = %q", prompt.Text)
	}
	agent.turns <- fakeTurn{}
}

// codexGoalCommand and claudeGoalCommand are the /goal each adapter advertises: Codex's takes
// pause and resume, Claude's only an objective or clear.
var (
	codexGoalCommand = entry("available_commands_update", map[string]any{"sessionUpdate": "available_commands_update",
		"availableCommands": []map[string]any{{"name": "goal", "input": map[string]string{"hint": "[<objective>|clear|pause|resume]"}}}})
	claudeGoalCommand = entry("available_commands_update", map[string]any{"sessionUpdate": "available_commands_update",
		"availableCommands": []map[string]any{{"name": "goal", "input": map[string]string{"hint": "[<condition> | clear]"}}}})
)

// claudeGoalCheck is the snapshot Claude's adapter sends each time its goal check blocks a stop.
func claudeGoalCheck(goal any) []byte {
	return MustRawJSON(map[string]any{"sessionUpdate": "session_info_update",
		"_meta": map[string]any{"jetbrains": map[string]any{"air": map[string]any{"goal": goal}}}})
}

func goalNotices(log *acp.EventLog) (titles []string) {
	for _, entry := range log.Snapshot() {
		var notice struct {
			Title string `json:"title"`
		}
		if entry.Kind == "notice" && json.Unmarshal(entry.Payload, &notice) == nil && strings.HasPrefix(notice.Title, "Goal") {
			titles = append(titles, notice.Title)
		}
	}
	return titles
}

func goalNow(conv *ConversationSession) session.Goal {
	conv.mu.Lock()
	defer conv.mu.Unlock()
	return *conv.goal
}

// Stopping a Claude goal clears it in the agent, which has no /goal pause, and keeps it paused
// here; resuming sets it again. Claude's adapter reports goals, an older one does not.
func TestStopPausesClaudeGoalByClearing(t *testing.T) {
	for _, reports := range []bool{false, true} {
		t.Run(fmt.Sprintf("reports=%v", reports), func(t *testing.T) { stopPausesClaudeGoal(t, reports) })
	}
}

func stopPausesClaudeGoal(t *testing.T, reports bool) {
	conv, agent := newFakeAgentSession(t, claudeGoalCommand)
	conv.nativeGoalReports = reports
	if err := conv.SetGoal(t.Context(), "ship it", "tests pass", nil); err != nil {
		t.Fatal(err)
	}
	command := agent.nextPrompt(t).Text
	if err := conv.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-agent.cancels
	agent.turns <- fakeTurn{stop: "cancelled"}
	agent.goalUntil(t, goalIn("paused"))
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal clear" {
		t.Fatalf("pause sent %q", prompt.Text)
	}
	agent.log.Append("session_info_update", 1, claudeGoalCheck(nil)) // the agent's answer to the clear
	agent.turns <- fakeTurn{}
	if goal := goalNow(conv); goal.State != "paused" {
		t.Fatalf("the agent's clear dropped the paused goal: %+v", goal)
	}
	if titles := goalNotices(agent.log); !slices.Equal(titles, []string{goalInterruptedNotice}) {
		t.Fatalf("notices = %q", titles)
	}
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if prompt := agent.nextPrompt(t); prompt.Text != command {
		t.Fatalf("resume sent %q, want %q", prompt.Text, command)
	}
	agent.turns <- fakeTurn{}
}

// The Pause button on Claude, which reports goals but cannot pause one, clears it in the agent
// and keeps it paused here, also after a worker restart.
func TestPausingClaudeGoalClearsAndKeepsIt(t *testing.T) {
	conv, agent := newFakeAgentSession(t, claudeGoalCommand)
	conv.configureACPGoal(map[string]any{"jetbrains": map[string]any{"air": map[string]any{
		"goal": map[string]any{"version": float64(1), "controlMethod": acp.GoalControlMethod}}}}, nil)
	if err := conv.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	agent.turns <- fakeTurn{}
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal clear" {
		t.Fatalf("pause sent %q", prompt.Text)
	}
	agent.log.Append("session_info_update", 1, claudeGoalCheck(nil))
	agent.turns <- fakeTurn{}
	waitConversationState(t, conv, "idle")
	if goal := goalNow(conv); goal.State != "paused" {
		t.Fatalf("goal = %+v", goal)
	}

	// A restarted worker replays the paused goal, or restores it from the row.
	paused := entry("goal_update", goalNow(conv))
	replayed, events := newFakeAgentSession(t, claudeGoalCommand, paused)
	replayed.nativeGoalReports = true
	events.log.Append("session_info_update", 1, claudeGoalCheck(nil))
	if goal := goalNow(replayed); goal.State != "paused" {
		t.Fatalf("replayed goal = %+v", goal)
	}
	restored, events := newFakeAgentSession(t, claudeGoalCommand)
	restored.nativeGoalReports = true
	events.log.Append("session_info_update", 1, claudeGoalCheck(nil))
	saved := goalNow(conv)
	restored.restoreGoal(&saved)
	if goal := goalNow(restored); goal.State != "paused" {
		t.Fatalf("restored goal = %+v", goal)
	}

	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal ship it" {
		t.Fatalf("resume sent %q", prompt.Text)
	}
	agent.log.Append("session_info_update", 1, claudeGoalCheck(nil)) // now the agent's clear counts
	agent.goalUntil(t, noGoal)
	agent.turns <- fakeTurn{}
}

// A message from the person during a native goal pauses it first, so the agent's goal check
// cannot hold the agent against them.
func TestUserMessagePausesNativeGoal(t *testing.T) {
	conv, agent := newFakeAgentSession(t, claudeGoalCommand)
	if err := conv.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t) // Claude's goal turn stays open
	if _, err := startPrompt(t.Context(), conv, acp.PromptInput{Text: "I'm doing it"}, false); err != nil {
		t.Fatal(err)
	}
	agent.goalUntil(t, goalIn("paused"))
	agent.turns <- fakeTurn{}
	for _, want := range []string{"/goal clear", "I'm doing it"} {
		if prompt := agent.nextPrompt(t); prompt.Text != want {
			t.Fatalf("sent %q, want %q", prompt.Text, want)
		}
		agent.turns <- fakeTurn{}
	}
	if titles := goalNotices(agent.log); !slices.Equal(titles, []string{goalInterruptedNotice}) {
		t.Fatalf("notices = %q", titles)
	}
}

func TestUserMessagesKeepCodexGoalActive(t *testing.T) {
	conv, agent := newFakeAgentSession(t, codexGoalCommand)
	var steered []string
	conv.steer = func(_ context.Context, input acp.PromptInput) (bool, error) {
		steered = append(steered, input.Text)
		agent.log.Append("prompt", 1, PromptLogPayload(input))
		return true, nil
	}
	if err := conv.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	t.Cleanup(func() {
		agent.turns <- fakeTurn{}
		agent.turns <- fakeTurn{}
	})

	if _, err := startPrompt(t.Context(), conv, acp.PromptInput{Text: "eta?"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrompt(t.Context(), conv, acp.PromptInput{Text: "check the logs first"}, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steered, []string{"check the logs first"}) {
		t.Fatalf("steered prompts = %q", steered)
	}
	if goal := goalNow(conv); goal.State != "active" || goal.LastReason != "" {
		t.Fatalf("goal = %+v", goal)
	}
	if titles := goalNotices(agent.log); len(titles) != 0 || len(agent.cancels) != 0 {
		t.Fatalf("messages produced notices %q and %d cancels", titles, len(agent.cancels))
	}
	agent.turns <- fakeTurn{}
	if got := agent.nextPrompt(t).Text; got != "eta?" {
		t.Fatalf("queued prompt = %q", got)
	}
}

func TestExplicitCodexGoalPauseUsesPrompt(t *testing.T) {
	conv, agent := newFakeAgentSession(t, codexGoalCommand)
	steered := make(chan acp.PromptInput, 1)
	conv.steer = func(_ context.Context, input acp.PromptInput) (bool, error) {
		steered <- input
		return true, nil
	}
	if err := conv.SetGoal(t.Context(), "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		agent.turns <- fakeTurn{}
		agent.turns <- fakeTurn{}
	})
	agent.nextPrompt(t)
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	agent.goalUntil(t, goalIn("paused"))
	select {
	case got := <-steered:
		t.Fatalf("sent goal control as steering input: %q", got.Text)
	default:
	}
	agent.turns <- fakeTurn{}
	if got := agent.nextPrompt(t).Text; got != "/goal pause" {
		t.Fatalf("goal pause sent %q", got)
	}
}

// A goal check that blocks the stop with the same reason while the agent does nothing is stopped
// after goalStopRuns blocks, once.
func TestRepeatingNativeGoalCheckStops(t *testing.T) {
	conv, agent := newFakeAgentSession(t, claudeGoalCommand)
	if err := conv.SetGoal(t.Context(), "ship it", "tests pass", nil); err != nil {
		t.Fatal(err)
	}
	agent.nextPrompt(t)
	check := func(iteration int, reason string) {
		agent.log.Append("session_info_update", 1, claudeGoalCheck(map[string]any{"objective": "ship it", "status": "active", "iterations": iteration, "lastReason": reason}))
	}
	check(1, "Tests have not run yet.")
	agent.log.Append("tool_call", 1, MustRawJSON(map[string]string{"toolCallId": "t"}))
	check(2, "Not satisfied: the user is doing it; tests still unverified.") // the agent worked
	check(3, "Not satisfied: the user is doing it, tests still unverified")
	check(4, "Not satisfied: the user is doing it; tests still unverified!")
	if goal := goalNow(conv); goal.State != "active" {
		t.Fatalf("stopped early: %+v", goal)
	}
	check(5, "Not satisfied: the user is doing it; tests are still unverified.") // the third idle cycle
	if goal := agent.goalUntil(t, goalIn("blocked")); !strings.Contains(goal.LastReason, "user is doing it") {
		t.Fatalf("goal = %+v", goal)
	}
	check(6, "Not satisfied: the user is doing it; tests still unverified.") // the agent catching up
	agent.turns <- fakeTurn{}
	if prompt := agent.nextPrompt(t); prompt.Text != "/goal clear" {
		t.Fatalf("sent %q", prompt.Text)
	}
	agent.log.Append("session_info_update", 1, claudeGoalCheck(nil))
	agent.turns <- fakeTurn{}
	if goal := goalNow(conv); goal.State != "blocked" {
		t.Fatalf("goal = %+v", goal)
	}
	if titles := goalNotices(agent.log); len(titles) != 1 || !strings.HasPrefix(titles[0], "Goal unmet") {
		t.Fatalf("notices = %q", titles)
	}
}
