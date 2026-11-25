package network

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/session"
)

var ErrNoGoal = errors.New("no goal is set")

// goalStopRuns is how many consecutive blocked or tool-free turns stop a gateway goal.
const goalStopRuns = 3

// setGoalState moves the goal and keeps active time: only time spent active accumulates.
func setGoalState(goal *session.Goal, state, reason string, now int64) {
	if goal.State == "active" && state != "active" {
		goal.ActiveMS += max(0, now-goal.ResumedAt)
		goal.ResumedAt = 0
	}
	if state == "active" && goal.State != "active" {
		goal.ResumedAt = now
	}
	goal.State = state
	if reason != "" {
		goal.LastReason = reason
	}
	goal.UpdatedAt = now
}

func goalActiveMS(goal *session.Goal, now int64) int64 {
	if goal.State == "active" {
		return goal.ActiveMS + max(0, now-goal.ResumedAt)
	}
	return goal.ActiveMS
}

// goalWantsTurn reports whether this worker should prompt for the goal. The caller holds mu.
func (cs *ConversationSession) goalWantsTurn() bool {
	return cs.goal != nil && cs.goal.State == "active" && cs.goal.Engine == "gateway" && !cs.userCancelled
}

// nativeGoalDeferral tells the agent's goal judge (Claude's Stop hook evaluates this text) that a
// person taking over ends the goal for now, so it stops blocking the agent's turns.
const nativeGoalDeferral = " If the user explicitly stops the work or puts it on hold, the goal is deferred: let the agent stop. Require no evidence beyond this literal condition."

func nativeGoalCommand(objective, checks string) string {
	command := "/goal " + objective
	if checks != "" {
		command += ". Done when: " + checks + "." + nativeGoalDeferral
	}
	return command
}

// sameGoalReason reports whether two goal-check reasons say nearly the same thing: at least 80%
// of their words in common.
func sameGoalReason(a, b string) bool {
	words := func(text string) map[string]bool {
		set := map[string]bool{}
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			set[word] = true
		}
		return set
	}
	left, right := words(a), words(b)
	common := 0
	for word := range left {
		if right[word] {
			common++
		}
	}
	union := len(left) + len(right) - common
	return union > 0 && common*5 >= union*4
}

// pauseNativeGoal tells the agent to stop pursuing its goal: /goal pause where its /goal takes
// that (Codex), else /goal clear (Claude's takes only an objective or clear). The goal stays
// paused here either way, so the person can resume it. Claude can clear a goal mid-turn even
// when the goal blocks every stop.
func (cs *ConversationSession) pauseNativeGoal(ctx context.Context) {
	cs.mu.Lock()
	command, steer := "/goal clear", cs.steer != nil
	if cs.nativeGoalPause || cs.nativeGoalReports {
		command = "/goal pause"
	}
	cs.mu.Unlock()
	_, _, _ = cs.Submit(ctx, acp.PromptInput{Text: command}, steer)
}

func (cs *ConversationSession) goalNotice(title string) {
	cs.Log.Append("notice", cs.clock().UnixMilli(), MustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "info", "title": title,
		"description": "Type /goal resume to continue it, or /goal clear to drop it.",
	}))
}

const goalInterruptedNotice = "Goal paused because you interrupted"

// pauseGoalForPrompt lets a person's message take over a native goal that can block every stop.
// Codex can take ordinary and steered messages while continuing its goal.
func (cs *ConversationSession) pauseGoalForPrompt(ctx context.Context) {
	cs.mu.Lock()
	goal := cs.goal
	if goal == nil || goal.Engine != "native" || goal.State != "active" || cs.closedErr() != nil ||
		cs.nativeGoalReports || cs.nativeGoalPause && cs.steer != nil {
		cs.mu.Unlock()
		return
	}
	setGoalState(goal, "paused", "paused because you sent a message", cs.clock().UnixMilli())
	cs.mu.Unlock()
	cs.publishGoal()
	cs.goalNotice(goalInterruptedNotice)
	cs.pauseNativeGoal(ctx)
}

// goalContinuation is the gateway engine's turn prompt, modelled on Codex's goal continuation.
func goalContinuation(goal *session.Goal) string {
	var prompt strings.Builder
	prompt.WriteString("Continue working toward this goal.\n\nGoal: ")
	prompt.WriteString(goal.Objective)
	if goal.Checks != "" {
		prompt.WriteString("\nDone when: ")
		prompt.WriteString(goal.Checks)
	}
	against := "the goal"
	if goal.Checks != "" {
		against = "the goal and its done-when criteria"
	}
	prompt.WriteString("\n\nBefore you stop, audit the work against " + against + `: check what is actually finished and verified, not what you intended. If anything remains and you can do it, do it now instead of describing it.

End your reply with exactly one final line, and nothing after it:
GOAL: met
GOAL: continue
GOAL: blocked <reason>

Use "met" only when the audit shows the goal is fully achieved, "blocked" only when you cannot make progress without the user, and "continue" otherwise.`)
	return prompt.String()
}

// goalVerdict reads the last "GOAL:" line of the agent's reply.
func goalVerdict(reply string) (verdict, reason string) {
	lines := strings.Split(reply, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Trim(lines[i], " \t\r*`_>")
		rest, ok := strings.CutPrefix(strings.ToUpper(line), "GOAL:")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(line[len(line)-len(rest):])
		word, reason, _ := strings.Cut(rest, " ")
		switch word = strings.ToLower(strings.Trim(word, "*`_.:")); word {
		case "met", "continue", "blocked":
			return word, strings.Trim(reason, " *`_")
		}
	}
	return "", ""
}

func goalLimitReached(goal *session.Goal, used int64, cost float64, now int64) string {
	limits := goal.Limits
	switch {
	case limits == nil:
		return ""
	case limits.Turns > 0 && goal.Turns >= limits.Turns:
		return "turn limit reached"
	case limits.Minutes > 0 && float64(goalActiveMS(goal, now)) >= limits.Minutes*60_000:
		return "time limit reached"
	case limits.Tokens > 0 && used >= limits.Tokens:
		return "token limit reached"
	case limits.CostUSD > 0 && cost >= limits.CostUSD:
		return "cost limit reached"
	}
	return ""
}

// observeGoalEntry tracks what the goal engines read from the log. The caller holds mu.
func (cs *ConversationSession) observeGoalEntry(entry acp.LogEntry) {
	switch entry.Kind {
	case "prompt":
		cs.turnText, cs.turnTools = cs.turnText[:0], 0
	case "tool_call":
		cs.turnTools++
	case "agent_message_chunk":
		var chunk struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		}
		if json.Unmarshal(entry.Payload, &chunk) == nil {
			cs.turnText = append(cs.turnText, chunk.Text.Text...)
			// Only the reply's last line matters.
			if over := len(cs.turnText) - 8<<10; over > 0 {
				cs.turnText = append(cs.turnText[:0], cs.turnText[over:]...)
			}
		}
	case "usage_update":
		var usage struct {
			Used int64 `json:"used"`
			Cost *struct {
				Amount float64 `json:"amount"`
			} `json:"cost"`
		}
		if json.Unmarshal(entry.Payload, &usage) == nil {
			cs.usageUsed = usage.Used
			if usage.Cost != nil {
				cs.usageCost = usage.Cost.Amount
			}
		}
	case "available_commands_update":
		var update struct {
			AvailableCommands []struct {
				Name  string `json:"name"`
				Input struct {
					Hint string `json:"hint"`
				} `json:"input"`
			} `json:"availableCommands"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil {
			cs.nativeGoal, cs.nativeGoalPause = false, false
			for _, command := range update.AvailableCommands {
				if strings.TrimPrefix(command.Name, "/") == "goal" {
					cs.nativeGoal, cs.nativeGoalPause = true, strings.Contains(command.Input.Hint, "pause")
				}
			}
		}
	case "session_info_update":
		if goal := acp.AIRGoal(entry.Payload); goal != nil {
			cs.applyNativeGoal(goal, entry.TS)
		}
	}
}

// applyNativeGoal follows the agent's snapshot. For a legacy goal, the agent's clear is how
// the worker pauses it, so its local paused or blocked state takes precedence. The caller holds mu.
func (cs *ConversationSession) applyNativeGoal(raw json.RawMessage, now int64) {
	reports := cs.nativeGoalReports
	held := cs.goal != nil && (cs.goal.State == "paused" || cs.goal.State == "blocked") && !reports
	if string(raw) == "null" {
		cs.nativeGoalAbsent = reports
		if cs.goal != nil && !held && (!reports || cs.goal.Engine == "native") {
			cleared := *cs.goal
			setGoalState(&cleared, "cleared", "", now)
			cs.goal, cs.goalCleared, cs.goalDirty = nil, &cleared, true
		}
		return
	}
	var snapshot struct {
		Objective  string  `json:"objective"`
		Status     string  `json:"status"`
		Iterations int     `json:"iterations"`
		LastReason string  `json:"lastReason"`
		CreatedAt  float64 `json:"createdAt"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return
	}
	state := map[string]string{"active": "active", "paused": "paused", "blocked": "blocked", "limited": "limited", "complete": "met"}[snapshot.Status]
	if state == "" || held && state == "active" {
		return
	}
	cs.nativeGoalAbsent = false
	goal := cs.goal
	if goal == nil || reports && goal.Engine != "native" {
		started := int64(snapshot.CreatedAt)
		if started > 0 && started < 1e12 {
			started *= 1000 // seconds
		}
		if started <= 0 {
			started = now
		}
		goal = &session.Goal{Engine: "native", State: "paused", StartedAt: started}
		cs.goal = goal
	}
	// Claude reports the whole condition sent, done-when criteria included.
	if snapshot.Objective != "" && (goal.Objective == "" || !strings.HasPrefix(snapshot.Objective, goal.Objective)) {
		goal.Objective = snapshot.Objective
	}
	// Each new iteration is the goal check blocking the agent's stop. The same reason again after
	// a cycle with no tool calls is a check the agent cannot satisfy (one that ignores the person
	// taking over): stop it rather than loop.
	if !reports && goal.State == "active" && state == "active" && snapshot.Iterations > cs.goalIter && snapshot.LastReason != "" {
		switch {
		case cs.turnTools > 0:
			cs.goalIdle = 0
		case sameGoalReason(goal.LastReason, snapshot.LastReason):
			cs.goalIdle++
		default:
			cs.goalIdle = 1
		}
		cs.turnTools = 0
		if cs.goalIdle >= goalStopRuns {
			state = "blocked"
			cs.goalPauseDue, cs.goalNoticeDue = true, "Goal unmet: the goal check kept repeating itself, so it was stopped"
		}
	}
	cs.goalIter = max(cs.goalIter, snapshot.Iterations)
	if snapshot.Iterations > goal.Turns {
		goal.Turns = snapshot.Iterations
	}
	if goal.State != state {
		setGoalState(goal, state, "", now)
	}
	if reports || snapshot.LastReason != "" {
		goal.LastReason = snapshot.LastReason
	}
	goal.UpdatedAt = now
	cs.goalDirty = true
}

// followNativeGoal infers completion from idle only for legacy goals. An adapter that
// advertises goal status can go idle between cycles of an active goal. The caller holds mu.
func (cs *ConversationSession) followNativeGoal(entry acp.LogEntry, state string) {
	goal := cs.goal
	if goal == nil || goal.Engine != "native" {
		return
	}
	switch {
	case state == string(StateIdle) && cs.state == string(StateWorking) && goal.State == "active" &&
		cs.userCancelled && cs.nativeGoalPauseRPC == nil:
		// A prompted turn is paused by finishTurn; unprompted work has no turn to finish.
		setGoalState(goal, "paused", "stopped by the user", entry.TS)
		cs.goalDirty, cs.goalPauseDue, cs.goalNoticeDue = true, true, goalInterruptedNotice
	case !cs.nativeGoalReports && state == string(StateIdle) && cs.state == string(StateWorking) && goal.State == "active" &&
		cs.stopReason != "error" && cs.stopReason != "failed" && cs.stopReason != "cancelled":
		setGoalState(goal, "met", "the agent finished", entry.TS)
		cs.goalSettled, cs.goalDirty = true, true
	case !cs.nativeGoalReports && state == string(StateWorking) && entry.Kind != "prompt" && goal.State == "met" && cs.goalSettled:
		setGoalState(goal, "active", "", entry.TS)
		goal.LastReason = ""
		cs.goalSettled, cs.goalDirty = false, true
	}
}

// finishTurn counts a goal turn and applies the stop rules once the agent's turn has ended.
func (cs *ConversationSession) finishTurn(ctx context.Context, err error) {
	cs.mu.Lock()
	if errors.Is(err, session.ErrSessionArchived) {
		cs.archived = true
	}
	goal := cs.goal
	if goal == nil || goal.State != "active" {
		cs.mu.Unlock()
		return
	}
	native := goal.Engine == "native"
	if native && cs.nativeGoalReports && (!cs.userCancelled || cs.nativeGoalPauseRPC != nil) {
		cs.mu.Unlock()
		return
	}
	now := cs.clock().UnixMilli()
	goal.Turns++
	goal.UpdatedAt = now
	switch {
	case cs.userCancelled:
		setGoalState(goal, "paused", "stopped by the user", now)
	case native:
	case cs.stopReason == "error" || cs.stopReason == "failed" || err != nil && !errors.Is(err, context.Canceled):
		setGoalState(goal, "blocked", "the agent turn failed", now)
	default:
		cs.applyGatewayStopRules(goal, now)
	}
	interrupted := cs.userCancelled
	cs.mu.Unlock()
	cs.publishGoal()
	if interrupted {
		cs.goalNotice(goalInterruptedNotice)
		if native {
			cs.pauseNativeGoal(ctx)
		}
	}
}

// applyGatewayStopRules reads the reply's verdict, progress and limits. The caller holds mu.
func (cs *ConversationSession) applyGatewayStopRules(goal *session.Goal, now int64) {
	verdict, reason := goalVerdict(string(cs.turnText))
	switch verdict {
	case "met":
		setGoalState(goal, "met", reason, now)
		return
	case "blocked":
		cs.goalBlocked++
		if reason == "" {
			reason = "the agent is blocked"
		}
		goal.LastReason = reason
		if cs.goalBlocked >= goalStopRuns {
			setGoalState(goal, "blocked", reason, now)
			return
		}
	default:
		cs.goalBlocked = 0
	}
	if cs.turnTools == 0 {
		cs.goalIdle++
		if cs.goalIdle >= goalStopRuns {
			setGoalState(goal, "blocked", "no progress", now)
			return
		}
	} else {
		cs.goalIdle = 0
	}
	if reason := goalLimitReached(goal, cs.usageUsed, cs.usageCost, now); reason != "" {
		setGoalState(goal, "limited", reason, now)
	}
}

// publishGoal logs the current goal unless it is unchanged since the last publish.
func (cs *ConversationSession) publishGoal() {
	cs.goalPublishMu.Lock()
	defer cs.goalPublishMu.Unlock()
	cs.mu.Lock()
	goal := cs.goal
	if goal == nil {
		goal = cs.goalCleared
	}
	if goal == nil {
		cs.mu.Unlock()
		return
	}
	payload := MustRawJSON(goal)
	if string(payload) == cs.goalPublished {
		cs.mu.Unlock()
		return
	}
	cs.goalPublished = string(payload)
	cs.mu.Unlock()
	// The sink saves it on the session row with the entry (conversationFold).
	cs.Log.Append("goal_update", cs.clock().UnixMilli(), payload)
}

// SetGoal replaces the goal and starts working on it.
func (cs *ConversationSession) SetGoal(ctx context.Context, objective, checks string, limits *session.GoalLimits) error {
	cs.mu.Lock()
	if err := cs.closedErr(); err != nil {
		cs.mu.Unlock()
		return err
	}
	now := cs.clock().UnixMilli()
	engine := "gateway"
	if cs.nativeGoal || cs.nativeGoalReports {
		engine = "native"
	}
	cs.goal = &session.Goal{Objective: objective, Checks: checks, State: "active", Engine: engine,
		StartedAt: now, ResumedAt: now, Limits: limits, UpdatedAt: now}
	cs.goalBlocked, cs.goalIdle, cs.goalSettled, cs.goalIter = 0, 0, false, 0
	cs.mu.Unlock()
	cs.publishGoal()
	if engine == "native" {
		_, _, err := cs.Submit(ctx, acp.PromptInput{Text: nativeGoalCommand(objective, checks)}, false)
		return err
	}
	cs.maybeDrain()
	return nil
}

// GoalChange is a PATCH: an action (pause or resume), or new objective, checks or limits.
type GoalChange struct {
	Action    string              `json:"action"`
	Objective *string             `json:"objective"`
	Checks    *string             `json:"checks"`
	Limits    *session.GoalLimits `json:"limits"`
}

func (cs *ConversationSession) UpdateGoal(ctx context.Context, change GoalChange) error {
	cs.mu.Lock()
	goal := cs.goal
	if goal == nil {
		cs.mu.Unlock()
		return ErrNoGoal
	}
	if err := cs.closedErr(); err != nil {
		cs.mu.Unlock()
		return err
	}
	now := cs.clock().UnixMilli()
	command, pause := "", false
	switch change.Action {
	case "pause":
		if goal.State == "active" {
			if control := cs.nativeGoalPauseRPC; goal.Engine == "native" && control != nil {
				cs.mu.Unlock()
				return cs.requestNativeGoalPause(ctx, control)
			}
			setGoalState(goal, "paused", "paused by the user", now)
			pause = true
		}
	case "resume":
		if goal.State != "active" {
			setGoalState(goal, "active", "", now)
			goal.LastReason = ""
			cs.goalBlocked, cs.goalIdle, cs.userCancelled = 0, 0, false
			command = "/goal resume"
			if !cs.nativeGoalPause && !cs.nativeGoalReports {
				// Pausing cleared it from an agent without /goal pause; set it again.
				command = nativeGoalCommand(goal.Objective, goal.Checks)
			}
		}
	case "":
		if change.Objective != nil && *change.Objective != goal.Objective || change.Checks != nil && *change.Checks != goal.Checks {
			if change.Objective != nil {
				goal.Objective = *change.Objective
			}
			if change.Checks != nil {
				goal.Checks = *change.Checks
			}
			command = nativeGoalCommand(goal.Objective, goal.Checks)
		}
		if change.Limits != nil {
			goal.Limits = change.Limits
		}
		goal.UpdatedAt = now
	default:
		cs.mu.Unlock()
		return errors.New("unknown goal action")
	}
	native := goal.Engine == "native"
	cs.mu.Unlock()
	cs.publishGoal()
	if native && pause {
		cs.pauseNativeGoal(ctx)
		return nil
	}
	if native && command != "" {
		_, _, err := cs.Submit(ctx, acp.PromptInput{Text: command}, false)
		return err
	}
	cs.maybeDrain()
	return nil
}

// restoreGoal adopts the goal saved on the session row when replaying the transcript found none:
// replay reads only a bounded tail, so a long chat's last goal update can lie before it.
func (cs *ConversationSession) restoreGoal(saved *session.Goal) {
	cs.mu.Lock()
	if saved == nil || saved.State == "cleared" || cs.goal != nil || cs.goalCleared != nil {
		cs.mu.Unlock()
		return
	}
	goal := *saved
	if cs.nativeGoalAbsent && goal.Engine == "native" {
		setGoalState(&goal, "cleared", "", cs.clock().UnixMilli())
		cs.goalCleared = &goal
		cs.mu.Unlock()
		cs.publishGoal()
		return
	}
	cs.goal = &goal
	cs.goalPublished = string(MustRawJSON(&goal))
	cs.mu.Unlock()
}

func (cs *ConversationSession) ClearGoal(ctx context.Context) error {
	cs.mu.Lock()
	goal := cs.goal
	if goal == nil {
		cs.mu.Unlock()
		return ErrNoGoal
	}
	cleared := *goal
	setGoalState(&cleared, "cleared", "", cs.clock().UnixMilli())
	cs.goal, cs.goalCleared = nil, &cleared
	native, closed := goal.Engine == "native", cs.closedErr() != nil
	cs.mu.Unlock()
	cs.publishGoal()
	if native && !closed {
		_, _, err := cs.Submit(ctx, acp.PromptInput{Text: "/goal clear"}, false)
		return err
	}
	return nil
}
