package network

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// Goal is a standing objective the agent works toward across turns. Every change is logged as
// goal_update and saved on the session row, so every viewer and peer sees the same goal.
type Goal struct {
	Objective  string      `json:"objective"`
	Checks     string      `json:"checks,omitempty"`
	State      string      `json:"state"`  // active, paused, blocked, limited, met or cleared
	Engine     string      `json:"engine"` // native (the agent's /goal command) or gateway (this worker's loop)
	StartedAt  int64       `json:"started_at"`
	ActiveMS   int64       `json:"active_ms"`            // active time before the current active stretch
	ResumedAt  int64       `json:"resumed_at,omitempty"` // set only while active
	Turns      int         `json:"turns"`
	LastReason string      `json:"last_reason,omitempty"`
	Limits     *GoalLimits `json:"limits,omitempty"`
	UpdatedAt  int64       `json:"updated_at"`
}

type GoalLimits struct {
	Minutes float64 `json:"minutes,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Tokens  int64   `json:"tokens,omitempty"`
	Turns   int     `json:"turns,omitempty"`
}

var errNoGoal = errors.New("no goal is set")

// goalStopRuns is how many consecutive blocked or tool-free turns stop a gateway goal.
const goalStopRuns = 3

// setGoalState moves the goal and keeps active time: only time spent active accumulates.
func setGoalState(goal *Goal, state, reason string, now int64) {
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

func goalActiveMS(goal *Goal, now int64) int64 {
	if goal.State == "active" {
		return goal.ActiveMS + max(0, now-goal.ResumedAt)
	}
	return goal.ActiveMS
}

// goalWantsTurn reports whether this worker should prompt for the goal. The caller holds mu.
func (session *ConversationSession) goalWantsTurn() bool {
	return session.goal != nil && session.goal.State == "active" && session.goal.Engine == "gateway" && !session.userCancelled
}

func nativeGoalCommand(objective, checks string) string {
	command := "/goal " + objective
	if checks != "" {
		command += ". Done when: " + checks
	}
	return command
}

// goalContinuation is the gateway engine's turn prompt, modelled on Codex's goal continuation.
func goalContinuation(goal *Goal) string {
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

func goalLimitReached(goal *Goal, used int64, cost float64, now int64) string {
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
func (session *ConversationSession) observeGoalEntry(entry acp.LogEntry) {
	switch entry.Kind {
	case "prompt":
		session.turnText, session.turnTools = session.turnText[:0], 0
	case "tool_call":
		session.turnTools++
	case "agent_message_chunk":
		var chunk struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		}
		if json.Unmarshal(entry.Payload, &chunk) == nil {
			session.turnText = append(session.turnText, chunk.Text.Text...)
			// Only the reply's last line matters.
			if over := len(session.turnText) - 8<<10; over > 0 {
				session.turnText = append(session.turnText[:0], session.turnText[over:]...)
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
			session.usageUsed = usage.Used
			if usage.Cost != nil {
				session.usageCost = usage.Cost.Amount
			}
		}
	case "available_commands_update":
		var update struct {
			AvailableCommands []struct {
				Name string `json:"name"`
			} `json:"availableCommands"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil {
			session.nativeGoal = slices.ContainsFunc(update.AvailableCommands, func(command struct {
				Name string `json:"name"`
			}) bool {
				return strings.TrimPrefix(command.Name, "/") == "goal"
			})
		}
	case "session_info_update":
		var update struct {
			Meta struct {
				JetBrains struct {
					Air struct {
						Goal json.RawMessage `json:"goal"`
					} `json:"air"`
				} `json:"jetbrains"`
			} `json:"_meta"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil && len(update.Meta.JetBrains.Air.Goal) > 0 {
			session.applyNativeGoal(update.Meta.JetBrains.Air.Goal, entry.TS)
		}
	}
}

// applyNativeGoal follows the agent's own goal snapshot. The caller holds mu.
func (session *ConversationSession) applyNativeGoal(raw json.RawMessage, now int64) {
	if string(raw) == "null" {
		if session.goal != nil {
			cleared := *session.goal
			setGoalState(&cleared, "cleared", "", now)
			session.goal, session.goalCleared, session.goalDirty = nil, &cleared, true
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
	if state == "" {
		return
	}
	goal := session.goal
	if goal == nil {
		started := int64(snapshot.CreatedAt)
		if started > 0 && started < 1e12 {
			started *= 1000 // seconds
		}
		if started <= 0 {
			started = now
		}
		goal = &Goal{Engine: "native", State: "paused", StartedAt: started}
		session.goal = goal
	}
	if snapshot.Objective != "" {
		goal.Objective = snapshot.Objective
	}
	if snapshot.Iterations > goal.Turns {
		goal.Turns = snapshot.Iterations
	}
	if goal.State != state {
		setGoalState(goal, state, "", now)
	}
	if snapshot.LastReason != "" {
		goal.LastReason = snapshot.LastReason
	}
	goal.UpdatedAt = now
	session.goalDirty = true
}

// followNativeGoal ends a native goal when the agent stops on its own, since no adapter reports it to
// us: Claude's /goal ends its turn, Codex keeps going without a prompt until its thread is idle.
// Unprompted work after that (Codex continuing) revives it. The caller holds mu.
func (session *ConversationSession) followNativeGoal(entry acp.LogEntry, state string) {
	goal := session.goal
	if goal == nil || goal.Engine != "native" {
		return
	}
	switch {
	case state == string(stateIdle) && session.state == string(stateWorking) && goal.State == "active" && session.userCancelled:
		// A prompted turn is paused by finishTurn; unprompted work has no turn to finish.
		setGoalState(goal, "paused", "stopped by the user", entry.TS)
		session.goalDirty, session.goalPauseDue = true, !session.running
	case state == string(stateIdle) && session.state == string(stateWorking) && goal.State == "active" &&
		session.stopReason != "error" && session.stopReason != "failed" && session.stopReason != "cancelled":
		setGoalState(goal, "met", "the agent finished", entry.TS)
		session.goalSettled, session.goalDirty = true, true
	case state == string(stateWorking) && entry.Kind != "prompt" && goal.State == "met" && session.goalSettled:
		setGoalState(goal, "active", "", entry.TS)
		goal.LastReason = ""
		session.goalSettled, session.goalDirty = false, true
	}
}

// finishTurn counts a goal turn and applies the stop rules once the agent's turn has ended.
func (session *ConversationSession) finishTurn(ctx context.Context, err error) {
	session.mu.Lock()
	if errors.Is(err, errSessionArchived) {
		session.archived = true
	}
	goal := session.goal
	if goal == nil || goal.State != "active" {
		session.mu.Unlock()
		return
	}
	now := session.clock().UnixMilli()
	goal.Turns++
	goal.UpdatedAt = now
	native := goal.Engine == "native"
	switch {
	case session.userCancelled:
		setGoalState(goal, "paused", "stopped by the user", now)
	case native:
	case session.stopReason == "error" || session.stopReason == "failed" || err != nil && !errors.Is(err, context.Canceled):
		setGoalState(goal, "blocked", "the agent turn failed", now)
	default:
		session.applyGatewayStopRules(goal, now)
	}
	paused := native && goal.State == "paused"
	session.mu.Unlock()
	session.publishGoal()
	if paused {
		_, _, _ = session.Submit(ctx, acp.PromptInput{Text: "/goal pause"}, false)
	}
}

// applyGatewayStopRules reads the reply's verdict, progress and limits. The caller holds mu.
func (session *ConversationSession) applyGatewayStopRules(goal *Goal, now int64) {
	verdict, reason := goalVerdict(string(session.turnText))
	switch verdict {
	case "met":
		setGoalState(goal, "met", reason, now)
		return
	case "blocked":
		session.goalBlocked++
		if reason == "" {
			reason = "the agent is blocked"
		}
		goal.LastReason = reason
		if session.goalBlocked >= goalStopRuns {
			setGoalState(goal, "blocked", reason, now)
			return
		}
	default:
		session.goalBlocked = 0
	}
	if session.turnTools == 0 {
		session.goalIdle++
		if session.goalIdle >= goalStopRuns {
			setGoalState(goal, "blocked", "no progress", now)
			return
		}
	} else {
		session.goalIdle = 0
	}
	if reason := goalLimitReached(goal, session.usageUsed, session.usageCost, now); reason != "" {
		setGoalState(goal, "limited", reason, now)
	}
}

// publishGoal logs and saves the current goal unless it is unchanged since the last publish.
func (session *ConversationSession) publishGoal() {
	session.goalPublishMu.Lock()
	defer session.goalPublishMu.Unlock()
	session.mu.Lock()
	goal := session.goal
	if goal == nil {
		goal = session.goalCleared
	}
	if goal == nil {
		session.mu.Unlock()
		return
	}
	payload := mustRawJSON(goal)
	if string(payload) == session.goalPublished {
		session.mu.Unlock()
		return
	}
	session.goalPublished = string(payload)
	var saved *Goal
	if session.goal != nil {
		copied := *session.goal
		saved = &copied
	}
	persist := session.goalChange
	session.mu.Unlock()
	session.log.Append("goal_update", session.clock().UnixMilli(), payload)
	if persist != nil {
		persist(saved)
	}
}

// SetGoal replaces the goal and starts working on it.
func (session *ConversationSession) SetGoal(ctx context.Context, objective, checks string, limits *GoalLimits) error {
	session.mu.Lock()
	if err := session.closedErr(); err != nil {
		session.mu.Unlock()
		return err
	}
	now := session.clock().UnixMilli()
	engine := "gateway"
	if session.nativeGoal {
		engine = "native"
	}
	session.goal = &Goal{Objective: objective, Checks: checks, State: "active", Engine: engine,
		StartedAt: now, ResumedAt: now, Limits: limits, UpdatedAt: now}
	session.goalBlocked, session.goalIdle, session.goalSettled = 0, 0, false
	session.mu.Unlock()
	session.publishGoal()
	if engine == "native" {
		_, _, err := session.Submit(ctx, acp.PromptInput{Text: nativeGoalCommand(objective, checks)}, false)
		return err
	}
	session.maybeDrain()
	return nil
}

// GoalChange is a PATCH: an action (pause or resume), or new objective, checks or limits.
type GoalChange struct {
	Action    string      `json:"action"`
	Objective *string     `json:"objective"`
	Checks    *string     `json:"checks"`
	Limits    *GoalLimits `json:"limits"`
}

func (session *ConversationSession) UpdateGoal(ctx context.Context, change GoalChange) error {
	session.mu.Lock()
	goal := session.goal
	if goal == nil {
		session.mu.Unlock()
		return errNoGoal
	}
	if err := session.closedErr(); err != nil {
		session.mu.Unlock()
		return err
	}
	now := session.clock().UnixMilli()
	command := ""
	switch change.Action {
	case "pause":
		if goal.State == "active" {
			setGoalState(goal, "paused", "paused by the user", now)
			command = "/goal pause"
		}
	case "resume":
		if goal.State != "active" {
			setGoalState(goal, "active", "", now)
			goal.LastReason = ""
			session.goalBlocked, session.goalIdle, session.userCancelled = 0, 0, false
			command = "/goal resume"
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
		session.mu.Unlock()
		return errors.New("unknown goal action")
	}
	native := goal.Engine == "native"
	session.mu.Unlock()
	session.publishGoal()
	if native && command != "" {
		_, _, err := session.Submit(ctx, acp.PromptInput{Text: command}, false)
		return err
	}
	session.maybeDrain()
	return nil
}

// restoreGoal adopts the goal saved on the session row when replaying the transcript found none:
// replay reads only a bounded tail, so a long chat's last goal update can lie before it.
func (session *ConversationSession) restoreGoal(saved *Goal) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if saved == nil || saved.State == "cleared" || session.goal != nil || session.goalCleared != nil {
		return
	}
	goal := *saved
	session.goal = &goal
	session.goalPublished = string(mustRawJSON(&goal))
}

func (session *ConversationSession) ClearGoal(ctx context.Context) error {
	session.mu.Lock()
	goal := session.goal
	if goal == nil {
		session.mu.Unlock()
		return errNoGoal
	}
	cleared := *goal
	setGoalState(&cleared, "cleared", "", session.clock().UnixMilli())
	session.goal, session.goalCleared = nil, &cleared
	native, closed := goal.Engine == "native", session.closedErr() != nil
	session.mu.Unlock()
	session.publishGoal()
	if native && !closed {
		_, _, err := session.Submit(ctx, acp.PromptInput{Text: "/goal clear"}, false)
		return err
	}
	return nil
}
