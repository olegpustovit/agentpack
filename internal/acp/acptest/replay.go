package acptest

import (
	"context"
	"encoding/json"
	"io"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
)

// Pair wires an in-process client and replay agent over io.Pipe.
func Pair(client sdkacp.Client) (clientConn *sdkacp.ClientSideConnection, agentConn *sdkacp.AgentSideConnection, cleanup func()) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	clientConn = sdkacp.NewClientSideConnection(client, c2aW, a2cR)
	agent := NewReplayAgent(nil)
	agentConn = sdkacp.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	cleanup = func() {
		_ = c2aW.Close()
		_ = a2cW.Close()
		_ = c2aR.Close()
		_ = a2cR.Close()
	}
	return clientConn, agentConn, cleanup
}

// ReplayAgent implements sdkacp.Agent by replaying fixture log entries on Prompt.
type ReplayAgent struct {
	entries            []acp.LogEntry
	conn               *sdkacp.AgentSideConnection
	permissionReplies  []json.RawMessage
	elicitationReplies []json.RawMessage
}

func NewReplayAgent(entries []acp.LogEntry) *ReplayAgent {
	return &ReplayAgent{entries: entries}
}

func (a *ReplayAgent) SetConnection(c *sdkacp.AgentSideConnection) { a.conn = c }

func (a *ReplayAgent) Authenticate(ctx context.Context, params sdkacp.AuthenticateRequest) (sdkacp.AuthenticateResponse, error) {
	return sdkacp.AuthenticateResponse{}, nil
}

func (a *ReplayAgent) Initialize(ctx context.Context, params sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error) {
	return sdkacp.InitializeResponse{
		ProtocolVersion:   sdkacp.ProtocolVersionNumber,
		AgentCapabilities: sdkacp.AgentCapabilities{LoadSession: false},
	}, nil
}

func (a *ReplayAgent) Logout(ctx context.Context, params sdkacp.LogoutRequest) (sdkacp.LogoutResponse, error) {
	return sdkacp.LogoutResponse{}, nil
}

func (a *ReplayAgent) Cancel(ctx context.Context, params sdkacp.CancelNotification) error {
	return nil
}

func (a *ReplayAgent) CloseSession(ctx context.Context, params sdkacp.CloseSessionRequest) (sdkacp.CloseSessionResponse, error) {
	return sdkacp.CloseSessionResponse{}, nil
}

func (a *ReplayAgent) ListSessions(ctx context.Context, params sdkacp.ListSessionsRequest) (sdkacp.ListSessionsResponse, error) {
	return sdkacp.ListSessionsResponse{}, nil
}

func (a *ReplayAgent) NewSession(ctx context.Context, params sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error) {
	return sdkacp.NewSessionResponse{SessionId: sdkacp.SessionId("test-session")}, nil
}

func (a *ReplayAgent) ResumeSession(ctx context.Context, params sdkacp.ResumeSessionRequest) (sdkacp.ResumeSessionResponse, error) {
	return sdkacp.ResumeSessionResponse{}, nil
}

func (a *ReplayAgent) SetSessionConfigOption(ctx context.Context, params sdkacp.SetSessionConfigOptionRequest) (sdkacp.SetSessionConfigOptionResponse, error) {
	return sdkacp.SetSessionConfigOptionResponse{}, nil
}

func (a *ReplayAgent) SetSessionMode(ctx context.Context, params sdkacp.SetSessionModeRequest) (sdkacp.SetSessionModeResponse, error) {
	return sdkacp.SetSessionModeResponse{}, nil
}

func (a *ReplayAgent) Prompt(ctx context.Context, params sdkacp.PromptRequest) (sdkacp.PromptResponse, error) {
	stop := sdkacp.StopReasonEndTurn
	var turnEndPayload json.RawMessage
	for _, entry := range a.entries {
		if err := a.replayEntry(ctx, params.SessionId, entry); err != nil {
			return sdkacp.PromptResponse{}, err
		}
		if entry.Kind == "turn_end" {
			stop = turnStopReason(entry.Payload)
			turnEndPayload = entry.Payload
		}
	}
	var meta map[string]any
	if len(turnEndPayload) != 0 {
		meta = replayMeta(turnEndPayload)
	}
	return sdkacp.PromptResponse{Meta: meta, StopReason: stop}, nil
}

func turnStopReason(payload json.RawMessage) sdkacp.StopReason {
	var turn struct {
		Status     string `json:"status"`
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(payload, &turn)
	reason := turn.StopReason
	if reason == "" {
		switch turn.Status {
		case "completed":
			reason = string(sdkacp.StopReasonEndTurn)
		case "cancelled":
			reason = string(sdkacp.StopReasonCancelled)
		default:
			reason = turn.Status
		}
	}
	switch sdkacp.StopReason(reason) {
	case sdkacp.StopReasonEndTurn, sdkacp.StopReasonMaxTokens, sdkacp.StopReasonMaxTurnRequests, sdkacp.StopReasonRefusal, sdkacp.StopReasonCancelled:
		return sdkacp.StopReason(reason)
	default:
		return sdkacp.StopReasonEndTurn
	}
}

func (a *ReplayAgent) replayEntry(ctx context.Context, session sdkacp.SessionId, entry acp.LogEntry) error {
	switch entry.Kind {
	case "permission_request":
		return a.replayPermission(ctx, session, entry.Payload)
	case "elicitation":
		return a.replayElicitation(ctx, session, entry.Payload)
	case "notice", "compaction_update", "compaction_summary_chunk":
		return a.conn.NotifyExtension(ctx, "_agentpack/replay_event", replayEvent{
			SessionId: session,
			Kind:      entry.Kind,
			Payload:   entry.Payload,
		})
	case "turn_end":
		return nil
	default:
		update, ok := logPayloadToUpdate(entry.Kind, entry.Payload)
		if !ok {
			return nil
		}
		return a.conn.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: session, Update: update})
	}
}

type replayEvent struct {
	SessionId sdkacp.SessionId `json:"sessionId"`
	Kind      string           `json:"kind"`
	Payload   json.RawMessage  `json:"payload"`
}

func (a *ReplayAgent) replayElicitation(ctx context.Context, session sdkacp.SessionId, payload json.RawMessage) error {
	var prompt struct {
		Prompt  string   `json:"prompt"`
		Choices []string `json:"choices"`
	}
	if err := json.Unmarshal(payload, &prompt); err != nil {
		return err
	}
	request := sdkacp.NewUnstableCreateElicitationRequestForm(sdkacp.UnstableElicitationSchema{
		Type: sdkacp.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{
			"value": map[string]any{"type": "string", "enum": prompt.Choices},
		},
		Required: []string{"value"},
	})
	request.Form.Message = prompt.Prompt
	request.Form.Meta = map[string]any{"agentpack/replayPayload": string(payload), "agentpack/sessionId": string(session)}
	response, err := a.conn.UnstableCreateElicitation(ctx, request)
	if err != nil {
		return err
	}
	reply, err := json.Marshal(response)
	if err != nil {
		return err
	}
	a.elicitationReplies = append(a.elicitationReplies, reply)
	return nil
}

func (a *ReplayAgent) replayPermission(ctx context.Context, session sdkacp.SessionId, payload json.RawMessage) error {
	var perm struct {
		RequestID string `json:"requestId"`
		Tool      string `json:"tool"`
		Summary   string `json:"summary"`
		Options   []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	}
	if err := json.Unmarshal(payload, &perm); err != nil {
		return err
	}
	opts := make([]sdkacp.PermissionOption, 0, len(perm.Options))
	for _, o := range perm.Options {
		kind := sdkacp.PermissionOptionKindAllowOnce
		if o.ID == "deny" {
			kind = sdkacp.PermissionOptionKindRejectOnce
		}
		opts = append(opts, sdkacp.PermissionOption{
			Kind:     kind,
			Name:     o.Label,
			OptionId: sdkacp.PermissionOptionId(o.ID),
		})
	}
	toolID := sdkacp.ToolCallId(perm.RequestID)
	if toolID == "" {
		toolID = "permission"
	}
	response, err := a.conn.RequestPermission(ctx, sdkacp.RequestPermissionRequest{
		Meta:      replayMeta(payload),
		SessionId: session,
		ToolCall: sdkacp.ToolCallUpdate{
			ToolCallId: toolID,
			Title:      sdkacp.Ptr(perm.Summary),
		},
		Options: opts,
	})
	if err != nil {
		return err
	}
	reply, err := json.Marshal(response)
	if err != nil {
		return err
	}
	a.permissionReplies = append(a.permissionReplies, reply)
	return nil
}

func logPayloadToUpdate(kind string, payload json.RawMessage) (sdkacp.SessionUpdate, bool) {
	switch kind {
	case "agent_message_chunk":
		update := sdkacp.UpdateAgentMessageText(acp.StripControls(chunkText(payload)))
		update.AgentMessageChunk.Meta = replayMeta(payload)
		return update, true
	case "agent_thought_chunk":
		update := sdkacp.UpdateAgentThoughtText(chunkText(payload))
		update.AgentThoughtChunk.Meta = replayMeta(payload)
		return update, true
	case "user_message_chunk":
		update := sdkacp.UpdateUserMessageText(chunkText(payload))
		update.UserMessageChunk.Meta = replayMeta(payload)
		return update, true
	case "tool_call":
		var tc sdkacp.SessionUpdateToolCall
		if json.Unmarshal(payload, &tc) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		tc.Meta = replayMeta(payload)
		return sdkacp.SessionUpdate{ToolCall: &tc}, true
	case "tool_call_update":
		var tu sdkacp.SessionToolCallUpdate
		if json.Unmarshal(payload, &tu) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		tu.Meta = replayMeta(payload)
		return sdkacp.SessionUpdate{ToolCallUpdate: &tu}, true
	case "plan":
		var plan struct {
			Entries []sdkacp.PlanEntry `json:"entries"`
		}
		if json.Unmarshal(payload, &plan) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update := sdkacp.UpdatePlan(plan.Entries...)
		update.Plan.Meta = replayMeta(payload)
		return update, true
	case "usage_update":
		var usage sdkacp.SessionUsageUpdate
		if json.Unmarshal(payload, &usage) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		usage.Meta = replayMeta(payload)
		usage.SessionUpdate = "usage_update"
		return sdkacp.SessionUpdate{UsageUpdate: &usage}, true
	case "available_commands_update":
		var update sdkacp.SessionAvailableCommandsUpdate
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{AvailableCommandsUpdate: &update}, true
	case "current_mode_update":
		var update sdkacp.SessionCurrentModeUpdate
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{CurrentModeUpdate: &update}, true
	case "config_option_update":
		var update sdkacp.SessionConfigOptionUpdate
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{ConfigOptionUpdate: &update}, true
	case "session_info_update":
		var update sdkacp.SessionSessionInfoUpdate
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{SessionInfoUpdate: &update}, true
	case "plan_update":
		var update sdkacp.SessionPlanUpdate
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{PlanUpdate: &update}, true
	case "plan_removed":
		var update sdkacp.SessionUpdatePlanRemoved
		if json.Unmarshal(payload, &update) != nil {
			return sdkacp.SessionUpdate{}, false
		}
		update.Meta = replayMeta(payload)
		update.SessionUpdate = kind
		return sdkacp.SessionUpdate{PlanRemoved: &update}, true
	default:
		return sdkacp.SessionUpdate{}, false
	}
}

func chunkText(payload json.RawMessage) string {
	var body struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Text.Text
}

func replayMeta(payload json.RawMessage) map[string]any {
	return map[string]any{"agentpack/replayPayload": string(payload)}
}

func replayPayloadFromMeta(meta map[string]any) json.RawMessage {
	if payload, ok := meta["agentpack/replayPayload"].(string); ok {
		return json.RawMessage(payload)
	}
	return nil
}
