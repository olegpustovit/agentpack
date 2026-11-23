package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
)

func rawUpdate(session, update string) []byte {
	return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + session + `","update":` + update + `}}`)
}

func lastPayload(t *testing.T, log *EventLog) (string, map[string]any) {
	t.Helper()
	entries := log.Snapshot()
	if len(entries) == 0 {
		t.Fatal("nothing logged")
	}
	entry := entries[len(entries)-1]
	var payload map[string]any
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("%s: %v", entry.Payload, err)
	}
	return entry.Kind, payload
}

func TestSubagentUpdatesAreLoggedAndChildEventsStamped(t *testing.T) {
	log := NewEventLog(0)
	client := NewHarnessClient("", log, nil)
	ctx := context.Background()

	announce := `{"sessionUpdate":"subagent_update","sessionId":"child-1","title":"Explore","capabilities":{"cancel":{}},"state":{"state":"running"}}`
	if !AppendRawSessionUpdate(log, rawUpdate("parent", announce)) {
		t.Fatal("subagent_update must be consumed before the SDK")
	}
	if entry := log.Snapshot()[0]; entry.Kind != "subagent_update" || string(entry.Payload) != announce {
		t.Fatalf("parent's announcement must be verbatim: %s %s", entry.Kind, entry.Payload)
	}
	// A grandchild announced on the child's stream belongs to the child.
	AppendRawSessionUpdate(log, rawUpdate("child-1", `{"sessionUpdate":"subagent_update","sessionId":"grandchild"}`))
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "child-1" || payload["sessionId"] != "grandchild" {
		t.Fatalf("nested announcement = %v", payload)
	}
	if !AppendRawSessionUpdate(log, rawUpdate("child-1", `{"sessionUpdate":"session_message","messageId":"m1","content":[{"type":"text","text":"Find it"}]}`)) {
		t.Fatal("session_message must be consumed")
	}
	if kind, payload := lastPayload(t, log); kind != "session_message" || payload[SubagentKey] != "child-1" {
		t.Fatalf("session_message = %s %v", kind, payload)
	}

	chunk := sdkacp.SessionNotification{SessionId: "child-1", Update: sdkacp.UpdateAgentMessageText("searching")}
	if err := client.SessionUpdate(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "child-1" || payload["text"].(map[string]any)["text"] != "searching" {
		t.Fatalf("child chunk = %v", payload)
	}
	tool := sdkacp.SessionNotification{SessionId: "child-1", Update: sdkacp.StartToolCall("c1", "Grep")}
	_ = client.SessionUpdate(ctx, tool)
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "child-1" || payload["toolCallId"] != "c1" {
		t.Fatalf("child tool call = %v", payload)
	}
	_ = client.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: "parent", Update: sdkacp.UpdateAgentMessageText("done")})
	if _, payload := lastPayload(t, log); payload[SubagentKey] != nil {
		t.Fatalf("parent chunk must not be stamped: %v", payload)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = client.RequestPermission(cancelled, sdkacp.RequestPermissionRequest{
		SessionId: "child-1",
		ToolCall:  sdkacp.ToolCallUpdate{ToolCallId: "c2"},
		Options:   []sdkacp.PermissionOption{{OptionId: "allow", Kind: sdkacp.PermissionOptionKindAllowOnce}},
	})
	// The cancelled context withdraws the request: its permission_result follows the request.
	entries := log.Snapshot()
	request := entries[len(entries)-2]
	var payload map[string]any
	_ = json.Unmarshal(request.Payload, &payload)
	if request.Kind != "permission_request" || payload[SubagentKey] != "child-1" {
		t.Fatalf("child permission = %s %v", request.Kind, payload)
	}
	if state := ReduceLog(entries[:len(entries)-1]); !state.NeedsYou {
		t.Fatal("a pending child permission needs the user")
	}
	if state := ReduceLog(entries); state.NeedsYou {
		t.Fatal("a withdrawn child permission no longer needs the user")
	}
}

func TestFlattenedSubagentFallbacks(t *testing.T) {
	log := NewEventLog(0)
	client := NewHarnessClient("", log, nil)
	claude := sdkacp.UpdateAgentMessageText("inside task")
	claude.AgentMessageChunk.Meta = map[string]any{"claudeCode": map[string]any{"parentToolUseId": "toolu_1"}}
	_ = client.SessionUpdate(context.Background(), sdkacp.SessionNotification{SessionId: "s", Update: claude})
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "tool:toolu_1" {
		t.Fatalf("claude fallback = %v", payload)
	}
	opencode := sdkacp.StartToolCall("t1", "Read")
	opencode.ToolCall.Meta = map[string]any{"opencode/child-session": "ses_child"}
	_ = client.SessionUpdate(context.Background(), sdkacp.SessionNotification{SessionId: "s", Update: opencode})
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "ses_child" {
		t.Fatalf("opencode fallback = %v", payload)
	}
	if err := CancelSubagent(context.Background(), log, client.Broker, "tool:toolu_1"); !errors.Is(err, ErrSubagentNotCancellable) {
		t.Fatalf("flattened subagents are not cancellable: %v", err)
	}
}

func TestAIRDraftSubagentsBecomeSubagentUpdates(t *testing.T) {
	log := NewEventLog(0)
	client := NewHarnessClient("", log, nil)
	spawned := `{"sessionUpdate":"subagent_spawned","subagentSessionId":"task-1","name":"Explore","task":"Find the scrubber","prompt":"p","capabilities":{}}`
	if !AppendRawSessionUpdate(log, rawUpdate("parent", spawned)) {
		t.Fatal("subagent_spawned must be consumed before the SDK drops its fields")
	}
	if kind, payload := lastPayload(t, log); kind != "subagent_update" || payload["sessionId"] != "task-1" || payload["title"] != "Explore" ||
		payload["description"] != "Find the scrubber" || payload["state"].(map[string]any)["state"] != "running" || payload[SubagentKey] != nil {
		t.Fatalf("spawned = %s %v", kind, payload)
	}
	// The child's own traffic, which still carries Claude's parentToolUseId, is stamped with the child.
	chunk := sdkacp.UpdateAgentMessageText("searching")
	chunk.AgentMessageChunk.Meta = map[string]any{"claudeCode": map[string]any{"parentToolUseId": "toolu_1"}}
	_ = client.SessionUpdate(context.Background(), sdkacp.SessionNotification{SessionId: "task-1", Update: chunk})
	if _, payload := lastPayload(t, log); payload[SubagentKey] != "task-1" {
		t.Fatalf("child chunk = %v", payload)
	}
	AppendRawSessionUpdate(log, rawUpdate("parent", `{"sessionUpdate":"subagent_state_update","subagentSessionId":"task-1","state":"failed"}`))
	if kind, payload := lastPayload(t, log); kind != "subagent_update" || payload["state"].(map[string]any)["state"] != "idle" || payload["state"].(map[string]any)["stopReason"] != "failed" {
		t.Fatalf("state update = %s %v", kind, payload)
	}
}

func TestRawToolCallWithSessionContentIsConsumed(t *testing.T) {
	log := NewEventLog(0)
	if AppendRawSessionUpdate(log, rawUpdate("s", `{"sessionUpdate":"tool_call","toolCallId":"t1","content":[{"type":"content","content":{"type":"text","text":"x"}}]}`)) {
		t.Fatal("an ordinary tool call stays on the SDK path")
	}
	linked := `{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Task","content":[{"type":"session","sessionId":"child-1"}]}`
	if !AppendRawSessionUpdate(log, rawUpdate("s", linked)) {
		t.Fatal("the SDK cannot decode session content")
	}
	if entry := log.Snapshot()[0]; entry.Kind != "tool_call" || string(entry.Payload) != linked {
		t.Fatalf("linked tool call = %s %s", entry.Kind, entry.Payload)
	}
}

func TestChunksOfDifferentSubagentsNeverMerge(t *testing.T) {
	parent := json.RawMessage(`{"text":{"text":"a"}}`)
	child := stampSubagent(json.RawMessage(`{"text":{"text":"b"}}`), "child-1")
	if _, err := mergeTextChunks(parent, child); err == nil {
		t.Fatal("parent and child chunks merged")
	}
	merged, err := mergeTextChunks(child, child)
	if err != nil || string(merged) != `{"_subagent":"child-1","text":{"text":"bb"}}` {
		t.Fatalf("same-child merge = %s %v", merged, err)
	}
	state := ReduceLog([]LogEntry{
		{Kind: "prompt", Payload: json.RawMessage(`{"text":"go"}`)},
		{Kind: "agent_message_chunk", Payload: parent},
		{Kind: "agent_message_chunk", Payload: child},
		{Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)},
		{Kind: "agent_message_chunk", Payload: child},
	})
	if len(state.Items) != 3 {
		t.Fatalf("items = %+v", state.Items)
	}
	if state.Working {
		t.Fatal("a background subagent must not make the parent look busy")
	}

	// Eviction coalescing respects the same boundary.
	log := NewEventLog(120)
	log.Append("agent_message_chunk", 1, parent)
	log.Append("agent_message_chunk", 2, child)
	log.Append("agent_message_chunk", 3, child)
	for _, entry := range log.Snapshot() {
		if strings.Contains(string(entry.Payload), `"ab`) {
			t.Fatalf("parent chunk absorbed a child chunk: %s", entry.Payload)
		}
	}
}

func TestStampSubagentEmptyObject(t *testing.T) {
	if got := string(stampSubagent(json.RawMessage(`{}`), "c")); got != `{"_subagent":"c"}` {
		t.Fatal(got)
	}
}

func TestCancelSubagentSendsSessionCancelForTheChild(t *testing.T) {
	log := NewEventLog(0)
	client := NewHarnessClient("", log, nil)
	toAgent, fromClient := io.Pipe()
	fromAgent, _ := io.Pipe()
	client.NewClientSideConnection(fromClient, fromAgent)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := CancelSubagent(ctx, log, client.Broker, "child-1"); !errors.Is(err, ErrSubagentNotCancellable) {
		t.Fatalf("unannounced child: %v", err)
	}
	AppendRawSessionUpdate(log, rawUpdate("parent", `{"sessionUpdate":"subagent_update","sessionId":"child-2"}`))
	if err := CancelSubagent(ctx, log, client.Broker, "child-2"); !errors.Is(err, ErrSubagentNotCancellable) {
		t.Fatalf("child without the cancel capability: %v", err)
	}
	AppendRawSessionUpdate(log, rawUpdate("parent", `{"sessionUpdate":"subagent_update","sessionId":"child-1","capabilities":{"cancel":{}}}`))
	// A patch without capabilities keeps them.
	AppendRawSessionUpdate(log, rawUpdate("parent", `{"sessionUpdate":"subagent_update","sessionId":"child-1","state":{"state":"running"}}`))
	sent := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(toAgent).ReadString('\n')
		sent <- line
	}()
	if err := CancelSubagent(ctx, log, client.Broker, "child-1"); err != nil {
		t.Fatal(err)
	}
	line := <-sent
	if !strings.Contains(line, `"method":"session/cancel"`) || !strings.Contains(line, `"sessionId":"child-1"`) {
		t.Fatalf("sent %s", line)
	}
}

func TestSubagentFixtureReduces(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "acp", "subagents.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []LogEntry
	children := map[string]bool{}
	for line := range splitLines(data) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if id := payloadSubagent(entry.Payload); id != "" {
			children[id] = true
		}
		entries = append(entries, entry)
	}
	if !children["sub-explore"] || !children["sub-review"] {
		t.Fatalf("fixture children = %v", children)
	}
	state := ReduceLog(entries)
	if state.Working || state.NeedsYou {
		t.Fatalf("finished run: working=%v needs_you=%v", state.Working, state.NeedsYou)
	}
}
