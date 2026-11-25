package acp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
)

// HarnessClient is the agentpack ACP client: permissions via Broker, updates to EventLog, fs confined to workspace.
type HarnessClient struct {
	Root      string
	Log       *EventLog
	Broker    *Broker
	Terminals *TerminalPool

	// AutoApprove reports that the agent runs in a mode where the user chose never to be asked.
	// Some agents still request permission then (Claude Code for commands outside the project).
	AutoApprove func() bool

	// StoreImage saves images agents and tools send; nil shows them as a short text instead.
	StoreImage ImageStore

	requestPrefix   string
	requestSequence atomic.Uint64
	updates         *notificationGate
}

var _ sdkacp.Client = (*HarnessClient)(nil)
var _ sdkacp.ExtensionMethodHandler = (*HarnessClient)(nil)

// NewHarnessClient wires a client implementation for ClientSideConnection.
func NewHarnessClient(root string, log *EventLog, broker *Broker) *HarnessClient {
	if broker == nil {
		broker = NewBroker(nil, nil, nil, nil)
	}
	if log == nil {
		log = NewEventLog(0)
	}
	return &HarnessClient{Root: root, Log: log, Broker: broker, Terminals: NewTerminalPool(), requestPrefix: rand.Text()}
}
func (c *HarnessClient) nextRequestID(kind string) string {
	return kind + "-" + c.requestPrefix + "-" + strconv.FormatUint(c.requestSequence.Add(1), 10)
}

func (c *HarnessClient) permissionRequestID(toolCallID sdkacp.ToolCallId) (string, error) {
	if toolCallID == "" {
		return "", errors.New("permission request is missing a tool call ID")
	}
	return c.nextRequestID("permission"), nil
}

// NewClientSideConnection returns an SDK client connection over the given transport.
func (c *HarnessClient) NewClientSideConnection(peerInput io.Writer, peerOutput io.Reader) *sdkacp.ClientSideConnection {
	c.updates = newNotificationGate(peerOutput, c.Log)
	conn := sdkacp.NewClientSideConnection(gatedClient{Client: c, gate: c.updates}, peerInput, c.updates)
	c.Broker.setCancelSession(func(ctx context.Context, sessionID string) error {
		return conn.Cancel(ctx, sdkacp.CancelNotification{SessionId: sdkacp.SessionId(sessionID)})
	})
	return conn
}

// DrainSessionUpdates waits for queued handlers after the connection reader has stopped.
// SDK Done closes before those handlers finish; session/load already has its own SDK barrier.
func (c *HarnessClient) DrainSessionUpdates(ctx context.Context) error {
	g := c.updates
	if g == nil {
		return errors.New("ACP connection is not initialized")
	}
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.inflight != 0 && ctx.Err() == nil {
		g.cond.Wait()
	}
	return ctx.Err()
}

func (c *HarnessClient) RequestPermission(ctx context.Context, p sdkacp.RequestPermissionRequest) (sdkacp.RequestPermissionResponse, error) {
	id, err := c.permissionRequestID(p.ToolCall.ToolCallId)
	if err != nil {
		return sdkacp.RequestPermissionResponse{}, err
	}
	allow, canAllow := allowOnce(p.Options)
	if canAllow && c.AutoApprove != nil && c.AutoApprove() {
		return sdkacp.RequestPermissionResponse{
			Outcome: sdkacp.RequestPermissionOutcome{Selected: &sdkacp.RequestPermissionOutcomeSelected{OptionId: allow}},
		}, nil
	}
	if err := c.Broker.Begin(id); err != nil {
		return sdkacp.RequestPermissionResponse{}, err
	}
	defer c.Broker.End(id)
	subagent := subagentID(c.Log.subagentSession(string(p.SessionId)), p.ToolCall.Meta)
	c.Log.Append("permission_request", time.Now().UnixMilli(), stampSubagent(permissionRequestPayload(id, p), subagent))
	if canAllow {
		c.Broker.markPermissionReady(id, string(allow))
		// The mode may have changed while the request was being published.
		if c.AutoApprove != nil && c.AutoApprove() {
			_ = c.Broker.ResolvePermission(ctx, c.Log, id, string(allow))
		}
	}
	opt, err := c.waitAnswer(ctx, "permission", "requestId", id)
	if err != nil {
		return sdkacp.RequestPermissionResponse{}, err
	}
	for _, choice := range p.Options {
		if string(choice.OptionId) == opt {
			return sdkacp.RequestPermissionResponse{
				Outcome: sdkacp.RequestPermissionOutcome{
					Selected: &sdkacp.RequestPermissionOutcomeSelected{OptionId: choice.OptionId},
				},
			}, nil
		}
	}
	return sdkacp.RequestPermissionResponse{
		Outcome: sdkacp.RequestPermissionOutcome{
			Cancelled: &sdkacp.RequestPermissionOutcomeCancelled{},
		},
	}, nil
}

func (c *HarnessClient) SessionUpdate(ctx context.Context, n sdkacp.SessionNotification) error {
	kind, payload := sessionUpdateLogEntry(n.Update, c.StoreImage)
	if kind == "" {
		return nil
	}
	subagent := subagentID(c.Log.subagentSession(string(n.SessionId)), sessionUpdateMeta(n.Update))
	c.Log.Append(kind, time.Now().UnixMilli(), stampSubagent(payload, subagent))
	return nil
}

func (c *HarnessClient) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "_agentpack/cursor/ask_question":
		return c.cursorAskQuestion(ctx, params)
	case "_agentpack/cursor/create_plan":
		return c.cursorCreatePlan(ctx, params)
	default:
		return nil, sdkacp.NewMethodNotFound(method)
	}
}

type cursorAskQuestionRequest struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title,omitempty"`
	Questions  []struct {
		ID            string `json:"id"`
		Prompt        string `json:"prompt"`
		AllowMultiple *bool  `json:"allowMultiple,omitempty"`
		Options       []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	} `json:"questions"`
}

func (c *HarnessClient) cursorAskQuestion(ctx context.Context, params json.RawMessage) (any, error) {
	var request cursorAskQuestionRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	if request.ToolCallID == "" || len(request.Questions) == 0 {
		return nil, errors.New("cursor ask_question requires a toolCallId and questions")
	}
	answers := make([]map[string]any, 0, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || question.Prompt == "" || len(question.Options) == 0 {
			return nil, errors.New("cursor question requires an id, prompt, and options")
		}
		choices := make([]string, 0, len(question.Options))
		optionIDs := make([]string, 0, len(question.Options))
		valid := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			if option.ID == "" || option.Label == "" {
				return nil, errors.New("cursor question option requires an id and label")
			}
			choices = append(choices, option.Label)
			optionIDs = append(optionIDs, option.ID)
			valid[option.ID] = struct{}{}
		}
		payload := map[string]any{
			"toolCallId": request.ToolCallID,
			"questionId": question.ID,
			"prompt":     question.Prompt,
			"choices":    choices,
			"optionIds":  optionIDs,
		}
		if request.Title != "" {
			payload["title"] = request.Title
		}
		if question.AllowMultiple != nil {
			payload["allowMultiple"] = *question.AllowMultiple
		}
		value, err := c.waitElicitation(ctx, payload)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		if err != nil {
			return nil, err
		}
		var selected []string
		if question.AllowMultiple != nil && *question.AllowMultiple {
			if err := json.Unmarshal([]byte(value), &selected); err != nil {
				return nil, errors.New("cursor multiple-choice answer must be a JSON string array")
			}
		} else {
			selected = []string{value}
		}
		for _, id := range selected {
			if _, ok := valid[id]; !ok {
				return nil, errors.New("cursor answer contains an unknown option")
			}
		}
		answers = append(answers, map[string]any{
			"questionId":        question.ID,
			"selectedOptionIds": selected,
		})
	}
	return map[string]any{"outcome": map[string]any{
		"outcome": "answered",
		"answers": answers,
	}}, nil
}

type cursorCreatePlanRequest struct {
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name,omitempty"`
	Overview   string          `json:"overview,omitempty"`
	Plan       string          `json:"plan"`
	Todos      json.RawMessage `json:"todos"`
	IsProject  *bool           `json:"isProject,omitempty"`
	Phases     json.RawMessage `json:"phases,omitempty"`
}

func (c *HarnessClient) cursorCreatePlan(ctx context.Context, params json.RawMessage) (any, error) {
	var request cursorCreatePlanRequest
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	var todos []json.RawMessage
	if request.ToolCallID == "" || request.Plan == "" || json.Unmarshal(request.Todos, &todos) != nil || todos == nil {
		return nil, errors.New("cursor create_plan requires a toolCallId, plan, and todos")
	}
	payload := map[string]any{
		"toolCallId": request.ToolCallID,
		"prompt":     request.Plan,
		"plan":       request.Plan,
		"choices":    []string{"Accept plan", "Reject plan"},
		"todos":      request.Todos,
	}
	if request.Name != "" {
		payload["title"] = request.Name
	}
	if request.Overview != "" {
		payload["overview"] = request.Overview
	}
	if request.IsProject != nil {
		payload["isProject"] = *request.IsProject
	}
	if len(request.Phases) > 0 {
		payload["phases"] = request.Phases
	}
	answer, err := c.waitElicitation(ctx, payload)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	if err != nil {
		return nil, err
	}
	switch answer {
	case "Accept plan":
		return map[string]any{"outcome": map[string]any{"outcome": "accepted"}}, nil
	case "Reject plan":
		return map[string]any{"outcome": map[string]any{"outcome": "rejected"}}, nil
	default:
		return nil, errors.New("cursor plan answer must accept or reject the plan")
	}
}

func (c *HarnessClient) waitElicitation(ctx context.Context, payload map[string]any) (string, error) {
	id := c.nextRequestID("elicitation")
	if err := c.Broker.Begin(id); err != nil {
		return "", err
	}
	defer c.Broker.End(id)
	payload["elicitationId"] = id
	c.Log.Append("elicitation", time.Now().UnixMilli(), mustJSON(payload))
	return c.waitAnswer(ctx, "elicitation", "elicitationId", id)
}

// waitAnswer waits for the user. When the agent withdraws the request ($/cancel_request), it
// records that, so the thread no longer waits for an answer nobody can give.
func (c *HarnessClient) waitAnswer(ctx context.Context, kind, idField, id string) (string, error) {
	value, err := c.Broker.Wait(ctx, id)
	if err != nil && ctx.Err() != nil {
		c.Log.Append(kind+"_result", time.Now().UnixMilli(), mustJSON(map[string]any{idField: id, "cancelled": true}))
	}
	return value, err
}

func (c *HarnessClient) ReadTextFile(ctx context.Context, p sdkacp.ReadTextFileRequest) (sdkacp.ReadTextFileResponse, error) {
	content, err := ReadWorkspaceFile(c.Root, nil, p.Path, p.Line, p.Limit)
	if err != nil {
		return sdkacp.ReadTextFileResponse{}, err
	}
	return sdkacp.ReadTextFileResponse{Content: content}, nil
}

func (c *HarnessClient) WriteTextFile(ctx context.Context, p sdkacp.WriteTextFileRequest) (sdkacp.WriteTextFileResponse, error) {
	if err := WriteWorkspaceFile(c.Root, nil, p.Path, p.Content); err != nil {
		return sdkacp.WriteTextFileResponse{}, err
	}
	return sdkacp.WriteTextFileResponse{}, nil
}

func (c *HarnessClient) CreateTerminal(ctx context.Context, p sdkacp.CreateTerminalRequest) (sdkacp.CreateTerminalResponse, error) {
	if c.Terminals == nil {
		c.Terminals = NewTerminalPool()
	}
	id, err := c.Terminals.create(c.Root, nil, p)
	if err != nil {
		return sdkacp.CreateTerminalResponse{}, err
	}
	return sdkacp.CreateTerminalResponse{TerminalId: id}, nil
}

func (c *HarnessClient) KillTerminal(ctx context.Context, p sdkacp.KillTerminalRequest) (sdkacp.KillTerminalResponse, error) {
	if c.Terminals == nil {
		return sdkacp.KillTerminalResponse{}, errUnknownTerminal
	}
	return sdkacp.KillTerminalResponse{}, c.Terminals.kill(p.TerminalId)
}

func (c *HarnessClient) ReleaseTerminal(ctx context.Context, p sdkacp.ReleaseTerminalRequest) (sdkacp.ReleaseTerminalResponse, error) {
	if c.Terminals == nil {
		return sdkacp.ReleaseTerminalResponse{}, errUnknownTerminal
	}
	return sdkacp.ReleaseTerminalResponse{}, c.Terminals.release(ctx, p.TerminalId)
}

func (c *HarnessClient) TerminalOutput(ctx context.Context, p sdkacp.TerminalOutputRequest) (sdkacp.TerminalOutputResponse, error) {
	if c.Terminals == nil {
		return sdkacp.TerminalOutputResponse{}, errUnknownTerminal
	}
	return c.Terminals.output(p.TerminalId)
}

func (c *HarnessClient) WaitForTerminalExit(ctx context.Context, p sdkacp.WaitForTerminalExitRequest) (sdkacp.WaitForTerminalExitResponse, error) {
	if c.Terminals == nil {
		return sdkacp.WaitForTerminalExitResponse{}, errUnknownTerminal
	}
	return c.Terminals.wait(ctx, p.TerminalId)
}

func sessionUpdateLogEntry(u sdkacp.SessionUpdate, store ImageStore) (string, json.RawMessage) {
	switch {
	case u.AgentMessageChunk != nil:
		return "agent_message_chunk", marshalChunkPayload(u.AgentMessageChunk.Content, store)
	case u.AgentThoughtChunk != nil:
		return "agent_thought_chunk", marshalChunkPayload(u.AgentThoughtChunk.Content, store)
	case u.UserMessageChunk != nil:
		return "user_message_chunk", marshalChunkPayload(u.UserMessageChunk.Content, store)
	case u.ToolCall != nil:
		return "tool_call", toolContentPayload(mustJSON(u.ToolCall), store)
	case u.ToolCallUpdate != nil:
		return "tool_call_update", toolContentPayload(mustJSON(u.ToolCallUpdate), store)
	case u.Plan != nil:
		return "plan", mustJSON(u.Plan)
	case u.PlanUpdate != nil:
		return "plan_update", mustJSON(u.PlanUpdate)
	case u.PlanRemoved != nil:
		return "plan_removed", mustJSON(u.PlanRemoved)
	case u.AvailableCommandsUpdate != nil:
		return "available_commands_update", mustJSON(u.AvailableCommandsUpdate)
	case u.CurrentModeUpdate != nil:
		return "current_mode_update", mustJSON(u.CurrentModeUpdate)
	case u.ConfigOptionUpdate != nil:
		return "config_option_update", mustJSON(u.ConfigOptionUpdate)
	case u.SessionInfoUpdate != nil:
		return "session_info_update", mustJSON(u.SessionInfoUpdate)
	case u.UsageUpdate != nil:
		return "usage_update", mustJSON(u.UsageUpdate)
	default:
		return "", nil
	}
}

func marshalChunkPayload(block sdkacp.ContentBlock, store ImageStore) json.RawMessage {
	if block.Text != nil {
		return mustJSON(map[string]any{"text": map[string]string{"text": block.Text.Text}})
	}
	return chunkPayload(mustJSON(block), store)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func permissionRequestPayload(requestID string, p sdkacp.RequestPermissionRequest) json.RawMessage {
	tool := ""
	if p.ToolCall.Kind != nil {
		tool = string(*p.ToolCall.Kind)
	}
	summary := ""
	if p.ToolCall.Title != nil {
		summary = *p.ToolCall.Title
	}
	options := make([]map[string]string, 0, len(p.Options))
	for _, choice := range p.Options {
		options = append(options, map[string]string{"id": string(choice.OptionId), "label": choice.Name, "kind": string(choice.Kind)})
	}
	return mustJSON(map[string]any{
		"requestId":  requestID,
		"toolCallId": string(p.ToolCall.ToolCallId),
		"tool":       tool,
		"summary":    summary,
		"options":    options,
	})
}

// allowOnce picks the one-time allow option, else the persistent one. Once is preferred so an
// automatic answer never writes a standing rule into the agent's own settings.
func allowOnce(options []sdkacp.PermissionOption) (sdkacp.PermissionOptionId, bool) {
	for _, kind := range []sdkacp.PermissionOptionKind{sdkacp.PermissionOptionKindAllowOnce, sdkacp.PermissionOptionKindAllowAlways} {
		for _, option := range options {
			if option.Kind == kind {
				return option.OptionId, true
			}
		}
	}
	return "", false
}
