package acptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestReplayFullFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "acp", "full.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var turnEndPayload json.RawMessage
	var entries []acp.LogEntry
	const giantOutputSize = 1 << 20
	giantOutput := strings.Repeat("tool output\n", giantOutputSize/12)
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Kind == "turn_end" {
			turnEndPayload = entry.Payload
		}
		if entry.Kind == "tool_call_update" && bytes.Contains(entry.Payload, []byte(`"giant-fixture-output"`)) {
			// Expand the fixture marker without checking in a megabyte of repetitive output.
			entry.Payload, err = json.Marshal(map[string]any{
				"toolCallId": "giant-fixture-output",
				"title":      "Read generated bundle",
				"kind":       "read",
				"status":     "completed",
				"rawOutput":  giantOutput,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		entries = append(entries, entry)
	}
	const tinyChunkCount = 10000
	tinyChunk := json.RawMessage(`{"text":{"text":"x"}}`)
	for range tinyChunkCount {
		entries = append(entries, acp.LogEntry{Kind: "agent_message_chunk", Payload: tinyChunk})
	}

	root := t.TempDir()
	log := acp.NewEventLog(0)
	client := &replayTestClient{
		HarnessClient: acp.NewHarnessClient(root, nil, log, nil),
		permissions:   make(chan string, 1),
		rawPayloads:   make(map[string][][]byte),
	}
	agent := NewReplayAgent(entries)
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	clientConn := acp.NewClientConnection(client, c2aW, a2cR)
	agentConn := sdkacp.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	defer func() {
		_ = c2aW.Close()
		_ = c2aR.Close()
		_ = a2cW.Close()
		_ = a2cR.Close()
		_ = agentConn.Done()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // failure bound only; the replay takes ~0.8s, 3x under -race
	defer cancel()
	if _, err := clientConn.Initialize(ctx, sdkacp.InitializeRequest{ProtocolVersion: sdkacp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	sess, err := clientConn.NewSession(ctx, sdkacp.NewSessionRequest{Cwd: root, McpServers: []sdkacp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := clientConn.Prompt(ctx, sdkacp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []sdkacp.ContentBlock{sdkacp.TextBlock("go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prompt.StopReason != sdkacp.StopReasonEndTurn {
		t.Fatalf("prompt stop reason = %q, want %q", prompt.StopReason, sdkacp.StopReasonEndTurn)
	}
	turnPayload, ok := prompt.Meta["agentpack/replayPayload"].(string)
	if !ok || turnPayload != string(turnEndPayload) {
		t.Fatalf("turn-end reply payload = %v, want %s", prompt.Meta, turnEndPayload)
	}
	if id := <-client.permissions; id != "p1" {
		t.Fatalf("permission tool call ID = %q, want p1", id)
	}
	if got := len(agent.permissionReplies); got != 1 {
		t.Fatalf("permission replies = %d, want 1", got)
	}
	var permissionReply sdkacp.RequestPermissionResponse
	if err := json.Unmarshal(agent.permissionReplies[0], &permissionReply); err != nil {
		t.Fatal(err)
	}
	if permissionReply.Outcome.Selected == nil || permissionReply.Outcome.Selected.OptionId != "allow" {
		t.Fatalf("permission reply = %s, want selected allow", agent.permissionReplies[0])
	}
	if got := len(agent.elicitationReplies); got != 1 {
		t.Fatalf("elicitation replies = %d, want 1", got)
	}
	var elicitationReply sdkacp.UnstableCreateElicitationResponse
	if err := json.Unmarshal(agent.elicitationReplies[0], &elicitationReply); err != nil {
		t.Fatal(err)
	}
	if elicitationReply.Accept == nil || elicitationReply.Accept.Content["value"] != "Direct" {
		t.Fatalf("elicitation reply = %s, want accepted Direct", agent.elicitationReplies[0])
	}

	kinds := map[string]bool{}
	for _, entry := range log.Snapshot() {
		kinds[entry.Kind] = true
	}
	for _, kind := range []string{
		"agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan",
		"user_message_chunk", "permission_request", "elicitation", "notice", "compaction_update", "compaction_summary_chunk",
		"usage_update", "available_commands_update", "current_mode_update", "config_option_update",
		"session_info_update", "plan_update", "plan_removed",
	} {
		if !kinds[kind] {
			t.Errorf("replay did not produce %q", kind)
		}
	}

	wantPayloads := make(map[string]map[string]int)
	for _, entry := range entries {
		if entry.Kind == "turn_end" {
			continue
		}
		if wantPayloads[entry.Kind] == nil {
			wantPayloads[entry.Kind] = make(map[string]int)
		}
		wantPayloads[entry.Kind][string(entry.Payload)]++
	}
	for kind, want := range wantPayloads {
		got := make(map[string]int)
		for _, payload := range client.rawPayloads[kind] {
			got[string(payload)]++
		}
		for payload, count := range want {
			if got[payload] != count {
				t.Errorf("%s raw payload replay count = %d, want %d", kind, got[payload], count)
				break
			}
		}
	}
	if got := len(client.rawPayloads["agent_message_chunk"]); got != 10003 {
		t.Errorf("agent message chunks = %d, want 10003 (3 fixture chunks plus %d tiny chunks)", got, tinyChunkCount)
	}
	if got := len(client.rawPayloads["notice"]); got != 1 {
		t.Errorf("notice raw events = %d, want 1", got)
	}
	if got := len(client.rawPayloads["compaction_update"]); got != 2 {
		t.Errorf("compaction raw events = %d, want 2", got)
	}
	if got := len(client.rawPayloads["compaction_summary_chunk"]); got != 1 {
		t.Errorf("compaction summary chunks = %d, want 1", got)
	}
	if got := len(client.rawPayloads["usage_update"]); got != 1 {
		t.Errorf("usage raw events = %d, want 1", got)
	}
	foundGiant := false
	for _, entry := range log.Snapshot() {
		if entry.Kind != "tool_call_update" {
			continue
		}
		var update struct {
			RawOutput string `json:"rawOutput"`
		}
		if json.Unmarshal(entry.Payload, &update) == nil && update.RawOutput == giantOutput {
			foundGiant = true
		}
	}
	if !foundGiant {
		t.Fatal("giant tool output did not survive replay")
	}
	for _, entry := range log.Snapshot() {
		if entry.Kind == "agent_message_chunk" && bytes.Contains(entry.Payload, []byte("\x1b")) {
			t.Fatal("ANSI escape survived agent text sanitization")
		}
	}
}

func splitLines(data []byte) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i, b := range data {
			if b != '\n' {
				continue
			}
			if !yield(string(data[start:i])) {
				return
			}
			start = i + 1
		}
		if start < len(data) {
			yield(string(data[start:]))
		}
	}
}

type replayTestClient struct {
	*acp.HarnessClient
	permissions chan string
	mu          sync.Mutex // requests and notifications run on different goroutines
	rawPayloads map[string][][]byte
}

func (c *replayTestClient) addRaw(kind string, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rawPayloads[kind] = append(c.rawPayloads[kind], append([]byte(nil), payload...))
}

func (c *replayTestClient) SessionUpdate(ctx context.Context, params sdkacp.SessionNotification) error {
	var kind string
	var meta map[string]any
	switch {
	case params.Update.AgentMessageChunk != nil:
		kind, meta = "agent_message_chunk", params.Update.AgentMessageChunk.Meta
	case params.Update.AgentThoughtChunk != nil:
		kind, meta = "agent_thought_chunk", params.Update.AgentThoughtChunk.Meta
	case params.Update.UserMessageChunk != nil:
		kind, meta = "user_message_chunk", params.Update.UserMessageChunk.Meta
	case params.Update.ToolCall != nil:
		kind, meta = "tool_call", params.Update.ToolCall.Meta
	case params.Update.ToolCallUpdate != nil:
		kind, meta = "tool_call_update", params.Update.ToolCallUpdate.Meta
	case params.Update.Plan != nil:
		kind, meta = "plan", params.Update.Plan.Meta
	case params.Update.UsageUpdate != nil:
		kind, meta = "usage_update", params.Update.UsageUpdate.Meta
	case params.Update.AvailableCommandsUpdate != nil:
		kind, meta = "available_commands_update", params.Update.AvailableCommandsUpdate.Meta
	case params.Update.CurrentModeUpdate != nil:
		kind, meta = "current_mode_update", params.Update.CurrentModeUpdate.Meta
	case params.Update.ConfigOptionUpdate != nil:
		kind, meta = "config_option_update", params.Update.ConfigOptionUpdate.Meta
	case params.Update.SessionInfoUpdate != nil:
		kind, meta = "session_info_update", params.Update.SessionInfoUpdate.Meta
	case params.Update.PlanUpdate != nil:
		kind, meta = "plan_update", params.Update.PlanUpdate.Meta
	case params.Update.PlanRemoved != nil:
		kind, meta = "plan_removed", params.Update.PlanRemoved.Meta
	}
	if payload := replayPayloadFromMeta(meta); payload != nil {
		c.addRaw(kind, payload)
	}
	if usage := params.Update.UsageUpdate; usage != nil {
		if usage.SessionUpdate != "usage_update" || usage.Used != 120000 || usage.Size != 200000 || usage.Cost == nil || usage.Cost.Amount != 0.42 || usage.Cost.Currency != "USD" {
			return io.ErrUnexpectedEOF
		}
		if payload := replayPayloadFromMeta(meta); payload != nil {
			c.Log.Append(kind, 0, payload)
		}
		return nil
	}
	switch kind {
	case "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "plan_update", "plan_removed":
		if payload := replayPayloadFromMeta(meta); payload != nil {
			c.Log.Append(kind, 0, payload)
		}
		return nil
	}
	return c.HarnessClient.SessionUpdate(ctx, params)
}

func (c *replayTestClient) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_agentpack/replay_event" {
		return nil, sdkacp.NewMethodNotFound(method)
	}
	var event replayEvent
	if err := json.Unmarshal(params, &event); err != nil {
		return nil, err
	}
	c.addRaw(event.Kind, event.Payload)
	c.Log.Append(event.Kind, 0, event.Payload)
	return nil, nil
}

func (c *replayTestClient) RequestPermission(_ context.Context, params sdkacp.RequestPermissionRequest) (sdkacp.RequestPermissionResponse, error) {
	id := string(params.ToolCall.ToolCallId)
	c.permissions <- id
	if payload := replayPayloadFromMeta(params.Meta); payload != nil {
		c.addRaw("permission_request", payload)
		c.Log.Append("permission_request", 0, payload)
	}
	if id != "p1" || params.ToolCall.Title == nil || *params.ToolCall.Title != "Run go test on worker package" || len(params.Options) != 2 || params.Options[0].OptionId != "allow" {
		return sdkacp.RequestPermissionResponse{}, io.ErrUnexpectedEOF
	}
	return sdkacp.RequestPermissionResponse{
		Outcome: sdkacp.RequestPermissionOutcome{
			Selected: &sdkacp.RequestPermissionOutcomeSelected{OptionId: params.Options[0].OptionId},
		},
	}, nil
}

func (c *replayTestClient) UnstableCreateElicitation(_ context.Context, params sdkacp.UnstableCreateElicitationRequest) (sdkacp.UnstableCreateElicitationResponse, error) {
	if params.Form == nil {
		return sdkacp.UnstableCreateElicitationResponse{}, io.ErrUnexpectedEOF
	}
	payload := replayPayloadFromMeta(params.Form.Meta)
	if payload != nil {
		c.addRaw("elicitation", payload)
		c.Log.Append("elicitation", 0, payload)
	}
	if params.Form.Message != "Which tone should the copy use?" {
		return sdkacp.UnstableCreateElicitationResponse{}, io.ErrUnexpectedEOF
	}
	value, ok := params.Form.RequestedSchema.Properties["value"].(map[string]any)
	if !ok {
		return sdkacp.UnstableCreateElicitationResponse{}, io.ErrUnexpectedEOF
	}
	choices, ok := value["enum"].([]any)
	if !ok || len(choices) != 3 || choices[0] != "Direct" || choices[1] != "Friendly" || choices[2] != "Formal" {
		return sdkacp.UnstableCreateElicitationResponse{}, io.ErrUnexpectedEOF
	}
	return sdkacp.UnstableCreateElicitationResponse{
		Accept: &sdkacp.UnstableCreateElicitationAccept{
			Action:  "accept",
			Content: map[string]any{"value": "Direct"},
		},
	}, nil
}
