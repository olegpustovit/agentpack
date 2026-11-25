package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	"github.com/olegpustovit/agentpack/internal/session"
)

// goalWireAgent holds the prompt open so the test can distinguish a control request from a
// queued slash command. Goal updates take the normal SDK notification path.
type goalWireAgent struct {
	*heldAgent
	conn       *sdkacp.AgentSideConnection
	actions    []string
	loadStatus string // empty publishes goal: null when loading
	pauseError error
	cancelled  chan struct{}
}

func (a *goalWireAgent) Initialize(_ context.Context, req sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error) {
	if !strings.Contains(string(MustRawJSON(req.ClientCapabilities.Meta)), `"air":`) {
		return sdkacp.InitializeResponse{}, errors.New("client did not opt into AIR")
	}
	return sdkacp.InitializeResponse{
		ProtocolVersion:   sdkacp.ProtocolVersionNumber,
		AgentCapabilities: sdkacp.AgentCapabilities{LoadSession: true},
		Meta: map[string]any{"jetbrains": map[string]any{"air": map[string]any{
			"version": 1,
			"goal": map[string]any{
				"version": 1, "controlMethod": "_session/goal", "actions": a.actions,
			},
		}}},
	}, nil
}

func (a *goalWireAgent) LoadSession(ctx context.Context, req sdkacp.LoadSessionRequest) (sdkacp.LoadSessionResponse, error) {
	if err := a.goal(ctx, a.loadStatus); err != nil {
		return sdkacp.LoadSessionResponse{}, err
	}
	return sdkacp.LoadSessionResponse{}, a.conn.SessionUpdate(ctx, sdkacp.SessionNotification{
		SessionId: req.SessionId, Update: sdkacp.UpdateAgentMessageText("old transcript"),
	})
}

func (a *goalWireAgent) goal(ctx context.Context, status string) error {
	var snapshot map[string]any
	if status != "" {
		snapshot = map[string]any{
			"objective": "ship it", "status": status, "createdAt": 1710000000000,
			"controlMethod": "_session/goal",
		}
	}
	return a.sessionInfo(ctx, map[string]any{"jetbrains": map[string]any{
		"air": map[string]any{"version": 1, "goal": snapshot},
	}})
}

func (a *goalWireAgent) thread(ctx context.Context, status string) error {
	return a.sessionInfo(ctx, map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": status}}})
}

func (a *goalWireAgent) sessionInfo(ctx context.Context, meta map[string]any) error {
	return a.conn.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: "test-session",
		Update: sdkacp.SessionUpdate{SessionInfoUpdate: &sdkacp.SessionSessionInfoUpdate{
			SessionUpdate: "session_info_update", Meta: meta,
		}}})
}

func (a *goalWireAgent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_session/goal" {
		return a.heldAgent.HandleExtensionMethod(ctx, method, params)
	}
	a.record(method)
	var request struct {
		SessionID string `json:"sessionId"`
		Action    string `json:"action"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	if request.SessionID != "test-session" || request.Action != "pause" {
		return nil, errors.New("unexpected goal control params")
	}
	if a.pauseError != nil {
		return nil, a.pauseError
	}
	return struct{}{}, a.goal(ctx, "paused")
}

func (a *goalWireAgent) Cancel(ctx context.Context, request sdkacp.CancelNotification) error {
	err := a.heldAgent.Cancel(ctx, request)
	a.cancelled <- struct{}{}
	return err
}

func connectGoalWire(t *testing.T, log *acp.EventLog, agent *goalWireAgent, loadID string) (*ConversationSession, *fakeAgent) {
	t.Helper()
	input, toAgent := io.Pipe()
	output, fromAgent := io.Pipe()
	t.Cleanup(func() {
		_ = toAgent.Close()
		_ = fromAgent.Close()
		_ = input.Close()
		_ = output.Close()
	})
	agent.heldAgent = &heldAgent{
		ReplayAgent: acptest.NewReplayAgent(nil), received: make(chan struct{}, 4), release: make(chan struct{}),
		steering: "injected",
	}
	agent.cancelled = make(chan struct{}, 1)
	t.Cleanup(func() { close(agent.release) })
	agent.conn = sdkacp.NewAgentSideConnection(agent, fromAgent, input)
	client := acp.NewHarnessClient("", log, nil).NewClientSideConnection(toAgent, output)
	prompter := &acpPrompter{log: log, conn: client}
	conv := NewConversationSession(log, prompter.broker())
	conv.steer = prompter.steer
	log.SetReplaying(loadID != "")
	setup, err := initializeACPSession(t.Context(), client, t.TempDir(), loadID, "", "", chatTools{}, false,
		func(_ context.Context, init sdkacp.InitializeResponse) error {
			conv.configureACPGoal(init.Meta, prompter.pauseGoal)
			return nil
		})
	log.SetReplaying(false)
	if err != nil {
		t.Fatal(err)
	}
	prompter.ready(setup)
	return conv, &fakeAgent{log: log}
}

func awaitGoalWireSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("the adapter did not receive the request")
	}
}

func beginGoalWireTurn(t *testing.T, conv *ConversationSession, agent *goalWireAgent) {
	t.Helper()
	if err := conv.SetGoal(t.Context(), "ship it", "tests pass", nil); err != nil {
		t.Fatal(err)
	}
	awaitGoalWireSignal(t, agent.received)
	if err := agent.goal(t.Context(), "active"); err != nil {
		t.Fatal(err)
	}
}

func TestCodexGoalStaysActiveAfterETA(t *testing.T) {
	agent := &goalWireAgent{actions: []string{"set", "pause", "resume", "clear"}}
	conv, events := connectGoalWire(t, acp.NewEventLog(0), agent, "")
	beginGoalWireTurn(t, conv, agent)
	if _, err := startPrompt(t.Context(), conv, acp.PromptInput{Text: "ETA?"}, true); err != nil {
		t.Fatal(err)
	}
	agent.release <- struct{}{}
	waitConversationState(t, conv, "idle")
	if goal := goalNow(conv); goal.State != "active" || goal.Checks != "tests pass" {
		t.Fatalf("the steered turn changed the goal: %+v", goal)
	}
	// Another ETA lands after the first cycle's prompt has ended and before the next
	// autonomous cycle. Its ordinary prompt response is not a goal verdict either.
	if _, err := startPrompt(t.Context(), conv, acp.PromptInput{Text: "Updated ETA?"}, false); err != nil {
		t.Fatal(err)
	}
	awaitGoalWireSignal(t, agent.received)
	if err := agent.conn.SessionUpdate(t.Context(), sdkacp.SessionNotification{
		SessionId: "test-session", Update: sdkacp.UpdateAgentMessageText("About five minutes."),
	}); err != nil {
		t.Fatal(err)
	}
	agent.release <- struct{}{}
	waitConversationState(t, conv, "idle")
	if goal := goalNow(conv); goal.State != "active" {
		t.Fatalf("the ETA reply changed the goal: %+v", goal)
	}
	for _, status := range []string{"active", "idle"} {
		if err := agent.thread(t.Context(), status); err != nil {
			t.Fatal(err)
		}
		state := "idle"
		if status == "active" {
			state = "working"
		}
		waitConversationState(t, conv, state)
		if goal := goalNow(conv); goal.State != "active" || goal.Turns != 0 || goal.LastReason != "" {
			t.Fatalf("thread status %q changed the goal: %+v", status, goal)
		}
	}
	for _, transition := range [][2]string{{"limited", "limited"}, {"blocked", "blocked"}, {"active", "active"}, {"complete", "met"}} {
		if err := agent.goal(t.Context(), transition[0]); err != nil {
			t.Fatal(err)
		}
		events.goalUntil(t, goalIn(transition[1]))
	}
	if err := agent.thread(t.Context(), "active"); err != nil {
		t.Fatal(err)
	}
	waitConversationState(t, conv, "working")
	if goal := goalNow(conv); goal.State != "met" {
		t.Fatalf("later work revived a completed goal: %+v", goal)
	}
	if err := agent.goal(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	events.goalUntil(t, func(goal *session.Goal) bool { return goal == nil })
}

func TestCodexGoalPauseAndStopUseNativeControl(t *testing.T) {
	agent := &goalWireAgent{actions: []string{"pause"}}
	conv, events := connectGoalWire(t, acp.NewEventLog(0), agent, "")
	beginGoalWireTurn(t, conv, agent)
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	events.goalUntil(t, goalIn("paused"))
	agent.mu.Lock()
	methods := slices.Clone(agent.methods)
	agent.mu.Unlock()
	if !slices.Equal(methods, []string{"session/prompt", "_session/goal"}) ||
		len(queuedIDs(conv)) != 0 || conv.ConversationState() != "working" {
		t.Fatalf("Pause should use the RPC during the turn: calls=%v queue=%v state=%s",
			methods, queuedIDs(conv), conv.ConversationState())
	}
	if err := agent.goal(t.Context(), "active"); err != nil {
		t.Fatal(err)
	}
	events.goalUntil(t, goalIn("active"))
	agent.release <- struct{}{}
	waitConversationState(t, conv, "idle")
	if err := conv.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitGoalWireSignal(t, agent.cancelled)
	events.goalUntil(t, goalIn("paused"))
	agent.mu.Lock()
	methods = slices.Clone(agent.methods)
	agent.mu.Unlock()
	if !slices.Equal(methods, []string{"session/prompt", "_session/goal", "_session/goal", "session/cancel"}) {
		t.Fatalf("Stop between cycles should pause before cancelling: calls=%v", methods)
	}
	if err := agent.goal(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	events.goalUntil(t, func(goal *session.Goal) bool { return goal == nil })
}

func TestCodexGoalPauseErrorLeavesGoalActive(t *testing.T) {
	agent := &goalWireAgent{actions: []string{"pause"}, pauseError: sdkacp.NewMethodNotFound("_session/goal")}
	conv, _ := connectGoalWire(t, acp.NewEventLog(0), agent, "")
	beginGoalWireTurn(t, conv, agent)
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err == nil {
		t.Fatal("expected the adapter's pause failure")
	}
	if goal := goalNow(conv); goal.State != "active" || len(queuedIDs(conv)) != 0 {
		t.Fatalf("failed control changed the goal or queued a prompt: goal=%+v queue=%v", goal, queuedIDs(conv))
	}
}

func TestCodexGoalPauseFallsBackWhenActionIsAbsent(t *testing.T) {
	agent := &goalWireAgent{actions: []string{"set"}}
	conv, events := connectGoalWire(t, acp.NewEventLog(0), agent, "")
	beginGoalWireTurn(t, conv, agent)
	if err := conv.UpdateGoal(t.Context(), GoalChange{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	events.goalUntil(t, goalIn("paused"))
	agent.mu.Lock()
	methods := slices.Clone(agent.methods)
	agent.mu.Unlock()
	if !slices.Equal(methods, []string{"session/prompt"}) || len(queuedIDs(conv)) != 1 {
		t.Fatalf("expected a queued pause command: calls=%v queue=%v", methods, queuedIDs(conv))
	}
	agent.release <- struct{}{}
	awaitGoalWireSignal(t, agent.received)
	agent.release <- struct{}{}
	waitConversationState(t, conv, "idle")
}

func TestCodexGoalLoadKeepsCurrentSnapshot(t *testing.T) {
	for _, status := range []string{"active", ""} {
		t.Run("status="+status, func(t *testing.T) {
			log := acp.NewEventLog(0)
			log.Append("agent_message_chunk", 1, entry("agent_message_chunk", map[string]any{"text": map[string]string{"text": "saved"}}).Payload)
			agent := &goalWireAgent{actions: []string{"pause"}, loadStatus: status}
			conv, events := connectGoalWire(t, log, agent, "test-session")
			saved := &session.Goal{Objective: "ship it", Engine: "native", State: "met", LastReason: "the agent finished"}
			conv.restoreGoal(saved)
			if status == "" {
				events.goalUntil(t, func(goal *session.Goal) bool { return goal == nil })
			} else if goal := events.goalUntil(t, goalIn("active")); goal.LastReason != "" || goal.StartedAt != 1710000000000 {
				t.Fatalf("snapshot did not replace old inferred state: %+v", goal)
			}
			if kinds := logEntryKinds(log); kinds != "agent_message_chunk,session_info_update,goal_update" {
				t.Fatalf("load should append current goal state once without replaying the transcript: %s", kinds)
			}
		})
	}
}
