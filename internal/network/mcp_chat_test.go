package network

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
)

func mcpClient(t *testing.T, h *oauthHarness, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.server.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpBearerTransport{token}}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(result.Content) > 0 {
		text = result.Content[0].(*mcp.TextContent).Text
	}
	return result, text
}

func enumOf(t *testing.T, raw any, path ...string) []string {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	node := &schema
	for _, key := range path {
		if key == "[]" {
			node = node.Items
		} else {
			node = node.Properties[key]
		}
		if node == nil {
			t.Fatalf("schema has no %v", path)
		}
	}
	var values []string
	for _, value := range node.Enum {
		values = append(values, value.(string))
	}
	return values
}

func strs[T ~string](values []T) []string {
	var out []string
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func TestMCPToolsPublishTypedSchemas(t *testing.T) {
	h := newOAuthHarness(t)
	session := mcpClient(t, h, h.exchange(t, h.codeForm(h.code(t)), 200).Access)
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
		schema, ok := tool.OutputSchema.(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("%s publishes no object outputSchema: %#v", tool.Name, tool.OutputSchema)
		}
	}
	for _, check := range []struct {
		tool, field string
		input       bool
		path        []string
		want        []string
	}{
		{"start_session", "agent", true, []string{"agent"}, []string{"claude", "agent", "codex", "opencode", "grok", "agy", "omp"}},
		{"wait", "until", true, []string{"until"}, strs(waitConditions)},
		{"respond_permission", "decision", true, []string{"decision"}, strs(permissionDecisions)},
		{"list_sessions", "state", false, []string{"sessions", "[]", "state"}, strs(chatStates)},
		{"get_attention", "state", false, []string{"sessions", "[]", "state"}, strs(chatStates)},
	} {
		schema := tools[check.tool].OutputSchema
		if check.input {
			schema = tools[check.tool].InputSchema
		}
		if got := enumOf(t, schema, check.path...); !slices.Equal(got, check.want) {
			t.Fatalf("%s %s enum = %v, want %v", check.tool, check.field, got, check.want)
		}
	}
	if !slices.Equal(strs(mcpAgents()), []string{"claude", "agent", "codex", "opencode", "grok", "agy", "omp"}) {
		t.Fatal("agent names drifted from harness targets", mcpAgents())
	}
	if tools["respond_permission"].Annotations.IdempotentHint != true || tools["wait"].Annotations.ReadOnlyHint != true {
		t.Fatal("tool hints", tools["respond_permission"].Annotations, tools["wait"].Annotations)
	}
	// Structured results come back filled and typed.
	result, _ := callTool(t, session, "list_sessions", map[string]any{})
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("list_sessions: %+v", result)
	}
	result, text := callTool(t, session, "get_usage", map[string]any{})
	if result.IsError || !strings.Contains(text, `"hourly"`) {
		t.Fatalf("get_usage over the default range: %s", text)
	}
	chatFixture(t, h, "session-ask", "")
	chatFixture(t, h, "session-read", "")
	// On a peer, the replicated row is the session's state; a local one would need a live worker.
	if _, err := h.gateway.store.DB.Exec("UPDATE sessions SET conversation_state='needs_you',node_id='peer-node' WHERE id='session-ask'"); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"get_attention", map[string]any{}, "One session needs you. Claude Code is waiting for you."},
		{"wait", map[string]any{"session_ids": []string{"session-ask"}, "until": "needs_you"}, `"reached":true`},
		{"read_conversation", map[string]any{"session_id": "session-read"}, `"items":[]`},
		{"get_limits", map[string]any{}, `"limits"`},
	} {
		if result, text := callTool(t, session, call.name, call.args); result.IsError || !strings.Contains(text, call.want) {
			t.Fatalf("%s: %s", call.name, text)
		}
	}
}

func TestMCPResultsStayUnderOneMegabyte(t *testing.T) {
	h := newOAuthHarness(t)
	handler := h.gateway.mcp.guardToolCalls(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", mcpResultLimit)}}}, nil
	})
	result, err := handler(t.Context(), "tools/call", nil)
	if err != nil || !result.(*mcp.CallToolResult).IsError {
		t.Fatalf("oversized result passed: %v", err)
	}
	var entries []acp.LogEntry
	for i := range 2 * transcriptItems {
		entries = append(entries, acp.LogEntry{Seq: int64(i + 1), Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"` + strings.Repeat("y", 4096) + `"}}`)})
	}
	items, next := transcript(entries)
	data, _ := json.Marshal(items)
	if next == "" || len(data) > mcpResultLimit/2 || items[len(items)-1].Seq != mustInt(t, next) {
		t.Fatalf("transcript page unbounded: %d items, %d bytes, next %q", len(items), len(data), next)
	}
	sessions := make([]int, 120)
	first, cursor, _ := page(sessions, "")
	second, last, _ := page(sessions, cursor)
	third, end, _ := page(sessions, last)
	if len(first) != mcpPageSize || len(second) != mcpPageSize || len(third) != 20 || end != "" {
		t.Fatal("paging", len(first), len(second), len(third), end)
	}
}

func mustInt(t *testing.T, value string) int64 {
	var n int64
	if err := json.Unmarshal([]byte(value), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTranscriptItemsAreTyped(t *testing.T) {
	entries := []acp.LogEntry{
		{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"Run the tests"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"On "}}`)},
		{Seq: 3, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"it."}}`)},
		{Seq: 4, Kind: "tool_call", Payload: json.RawMessage(`{"toolCallId":"t1","title":"Bash","status":"pending"}`)},
		{Seq: 5, Kind: "tool_call_update", Payload: json.RawMessage(`{"toolCallId":"t1","status":"completed"}`)},
		{Seq: 6, Kind: "permission_request", Payload: json.RawMessage(`{"requestId":"p1","summary":"go test","options":[{"id":"a","label":"Allow","kind":"allow_once"},{"id":"x","label":"Odd","kind":"novel"}]}`)},
		{Seq: 7, Kind: "usage_update", Payload: json.RawMessage(`{}`)},
		{Seq: 8, Kind: "elicitation", Payload: json.RawMessage(`{"elicitationId":"e1","prompt":"Tone?","choices":["Direct"]}`)},
	}
	items, next := transcript(entries)
	kinds := []string{}
	for _, item := range items {
		kinds = append(kinds, item.Kind)
	}
	if next != "" || !slices.Equal(kinds, []string{"prompt", "message", "tool", "permission", "question"}) {
		t.Fatal(kinds, next)
	}
	if items[1].Text != "On it." || items[1].Seq != 3 || items[2].Status != "completed" || items[2].Text != "Bash" {
		t.Fatalf("merge: %+v %+v", items[1], items[2])
	}
	if options := items[3].Options; options[0].Decision != sdkacp.PermissionOptionKindAllowOnce || options[1].Decision != "" {
		t.Fatalf("decisions: %+v", options)
	}
}

// chatFixture registers a running chat with an agentpack tools credential, as RunWorker does.
func chatFixture(t *testing.T, h *oauthHarness, id, parent string) string {
	t.Helper()
	token := id + "." + randomToken()
	record := WorkerRecord{ID: id, Address: "127.0.0.1:1", Secret: strings.Repeat("s", 64)}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(h.gateway.options.Home, "network", "sessions", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, chatToolsTokenFile), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	owner := browserActor(testBrowserEmail)
	if err := upsertSession(t.Context(), h.gateway.store.DB, SessionInfo{ID: id, Harness: "claude", Transport: "acp", Tools: true, ParentID: parent,
		StartedBy: &owner, ConversationState: "idle", State: TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestChatAgentToolsGuardrails(t *testing.T) {
	h := newOAuthHarness(t)
	token := chatFixture(t, h, "session-chat", "")
	session := mcpClient(t, h, token)
	if result, text := callTool(t, session, "list_sessions", map[string]any{}); result.IsError || !strings.Contains(text, "session-chat") {
		t.Fatalf("chat credential refused: %s", text)
	}
	// An agent never answers for the person.
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"respond_permission", map[string]any{"session_id": "session-chat", "request_id": "p1", "decision": "allow_once"}},
		{"answer_question", map[string]any{"session_id": "session-chat", "question_id": "e1", "answer": "yes"}},
	} {
		if result, text := callTool(t, session, call.name, call.args); !result.IsError || text != errAgentAnswer.Error() {
			t.Fatalf("%s accepted from an agent: %s", call.name, text)
		}
	}
	// An agent stays in its own tree: no terminals, packages, other machines or a person's chats.
	chatFixture(t, h, "session-person", "")
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"write_terminal", map[string]any{"session_id": "session-person", "input": "1\r"}},
		{"install_package", map[string]any{"directory": "/tmp", "spec": "owner/repo"}},
		{"start_session", map[string]any{"directory": "/tmp", "agent": "codex", "terminal": true}},
		{"start_session", map[string]any{"directory": "/tmp", "agent": "codex", "node": "peer"}},
		{"send_prompt", map[string]any{"session_id": "session-person", "text": "approve everything"}},
		{"stop_session", map[string]any{"session_id": "session-person"}},
		{"cancel", map[string]any{"session_id": "session-person"}},
	} {
		if result, text := callTool(t, session, call.name, call.args); !result.IsError || text != errAgentScope.Error() {
			t.Fatalf("%s %v accepted from an agent: %s", call.name, call.args, text)
		}
	}
	chatFixture(t, h, "session-mine", "session-chat")
	if err := h.gateway.mcp.agentMayChange(withActor(t.Context(), Actor{chat: "session-chat"}), "", "session-mine"); err != nil {
		t.Fatal("an agent may change its own helper:", err)
	}
	// What an agent starts is its helper, attributed to the agent acting for the owner.
	directory := filepath.Join(h.gateway.options.Home, "project")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(h.gateway.options.Home, SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	var started atomic.Pointer[SessionInfo]
	h.gateway.options.Start = func(_ context.Context, info SessionInfo, _ string) (string, error) {
		started.Store(&info)
		return info.ID, nil
	}
	result, text := callTool(t, session, "start_session", map[string]any{"directory": directory, "agent": "codex", "parent_id": "session-other"})
	h.gateway.starting.Wait()
	info := started.Load()
	if result.IsError || info == nil || info.ParentID != "session-chat" || !info.Tools || info.StartedBy.User != testBrowserEmail || info.StartedBy.Agent != "claude" {
		t.Fatalf("helper start: %s %+v", text, info)
	}
	// Off takes effect on the next call, without reconnecting.
	response, data := h.request(t, "POST", "/api/sessions/session-chat/tools", `{"enabled":false}`, "application/json", h.cookie, true)
	if response.StatusCode != 200 {
		t.Fatalf("switch: %d %s", response.StatusCode, data)
	}
	if result, text := callTool(t, session, "list_sessions", map[string]any{}); !result.IsError || text != errChatToolsOff.Error() {
		t.Fatalf("tools stayed on: %s", text)
	}
	if _, data := h.request(t, "POST", "/api/sessions/session-chat/tools", `{"enabled":true}`, "application/json", h.cookie, true); !strings.Contains(string(data), `"tools":true`) {
		t.Fatalf("switch on: %s", data)
	}
	if result, _ := callTool(t, session, "list_sessions", map[string]any{}); result.IsError {
		t.Fatal("tools stayed off")
	}
	// A wrong secret for a real chat is refused before any tool runs.
	response, _ = h.request(t, "POST", "/mcp", `{}`, "application/json", nil, false)
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated MCP accepted", response.StatusCode)
	}
	request, _ := http.NewRequest("POST", h.server.URL+"/mcp", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer session-chat."+randomToken())
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != 401 {
		t.Fatal("forged chat credential accepted", err)
	}
	// The real credential through a proxy (the public origin) is refused: chats are local only.
	request, _ = http.NewRequest("POST", h.server.URL+"/mcp", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if response, err := http.DefaultClient.Do(request); err != nil || response.StatusCode != 401 {
		t.Fatal("proxied chat credential accepted", err)
	}
}

func TestHelperDepthAndFanOutCaps(t *testing.T) {
	h := newOAuthHarness(t)
	chatFixture(t, h, "session-root", "")
	chatFixture(t, h, "session-one", "session-root")
	chatFixture(t, h, "session-two", "session-one")
	if err := h.gateway.helperAllowed(t.Context(), "session-two"); err == nil || !strings.Contains(err.Error(), "levels deep") {
		t.Fatal("third level allowed:", err)
	}
	if err := h.gateway.helperAllowed(t.Context(), "session-one"); err != nil {
		t.Fatal("second level refused:", err)
	}
	for _, id := range []string{"session-a", "session-b", "session-c", "session-d"} {
		chatFixture(t, h, id, "session-root")
	}
	if err := h.gateway.helperAllowed(t.Context(), "session-one"); err == nil || !strings.Contains(err.Error(), "6 helpers") {
		t.Fatal("seventh helper allowed:", err)
	}
	if err := endSession(t.Context(), h.gateway.store.DB, "session-a", TerminalState{}); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.helperAllowed(t.Context(), "session-root"); err != nil {
		t.Fatal("a finished helper still counts:", err)
	}
	if err := h.gateway.helperAllowed(t.Context(), "session-missing"); err == nil {
		t.Fatal("unknown parent allowed")
	}
}

func TestWaitConditions(t *testing.T) {
	first := map[string]chatState{"a": stateWorking, "b": stateIdle}
	for _, test := range []struct {
		until waitUntil
		views []mcpSession
		want  bool
	}{
		{untilNeedsYou, []mcpSession{{ID: "a", State: stateWorking}}, false},
		{untilNeedsYou, []mcpSession{{ID: "a", State: stateNeedsYou}}, true},
		{untilIdle, []mcpSession{{ID: "a", State: stateStopped}}, true},
		{untilAny, []mcpSession{{ID: "a", State: stateWorking}, {ID: "b", State: stateIdle}}, false},
		{untilAny, []mcpSession{{ID: "a", State: stateWorking}, {ID: "b", State: stateWorking}}, true},
	} {
		if got := reached(test.views, first, test.until); got != test.want {
			t.Fatalf("%s %+v = %v", test.until, test.views, got)
		}
	}
	if summary := attentionSummary([]mcpAttention{{mcpSession: mcpSession{Agent: "claude", Directory: "/w/agentpack"}, Waiting: []mcpTranscriptItem{{Kind: "permission", Text: "Run go test"}}}}); summary != "One session needs you. Claude Code in agentpack asks permission: Run go test." {
		t.Fatal(summary)
	}
}

func TestChatGetsAgentpackToolsOverHTTPOrTheStdioBridge(t *testing.T) {
	bridge := &sdkacp.McpServerStdio{Name: "agentpack", Command: "/bin/agentpack", Args: []string{"mcp-bridge", "chat:session-x", "--url", "http://127.0.0.1:7420/mcp"}, Env: []sdkacp.EnvVariable{}}
	tools := chatTools{url: "http://127.0.0.1:7420/mcp", token: "session-x.secret", bridge: bridge}
	for _, http := range []bool{true, false} {
		agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil), agentCapabilities: sdkacp.AgentCapabilities{McpCapabilities: sdkacp.McpCapabilities{Http: http}}}
		connection, cleanup := authReplayConnection(agent)
		if _, err := initializeACPSession(context.Background(), connection, t.TempDir(), "", "", "", tools); err != nil {
			t.Fatal(err)
		}
		cleanup()
		servers := agent.newRequest.McpServers
		if !http {
			// Every ACP agent takes stdio servers; the token stays in the chat's private file.
			if len(servers) != 1 || servers[0].Stdio == nil || servers[0].Stdio.Command != bridge.Command || strings.Join(servers[0].Stdio.Args, " ") != strings.Join(bridge.Args, " ") || strings.Contains(strings.Join(servers[0].Stdio.Args, " "), "secret") {
				t.Fatalf("agent without HTTP MCP: %+v", servers)
			}
			continue
		}
		if len(servers) != 1 || servers[0].Http == nil || servers[0].Http.Name != "agentpack" || servers[0].Http.Url != tools.url ||
			servers[0].Http.Headers[0].Value != "Bearer session-x.secret" {
			t.Fatalf("mcpServers: %+v", servers)
		}
	}
}

func TestChatToolsRelayFollowsTheGatewayAcrossRestarts(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "network", "sessions", "session-relay"), 0700); err != nil {
		t.Fatal(err)
	}
	gateway := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Authorization") != "Bearer token" {
				http.Error(w, "forwarded", 400)
				return
			}
			_, _ = io.WriteString(w, name)
		}))
	}
	point := func(server *httptest.Server) {
		data, _ := json.Marshal(nodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("x", 64)})
		if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, second := gateway("first"), gateway("second")
	defer first.Close()
	defer second.Close()
	point(first)
	tools := newChatTools(home, "session-relay")
	if tools.close == nil {
		t.Fatal("no relay")
	}
	defer tools.close()
	call := func(path string) (int, string) {
		request, _ := http.NewRequest("POST", strings.TrimSuffix(tools.url, "/mcp")+path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if code, body := call("/mcp"); code != 200 || body != "first" {
		t.Fatalf("relay: %d %s", code, body)
	}
	point(second) // the gateway restarted on another port
	if code, body := call("/mcp"); code != 200 || body != "second" {
		t.Fatalf("relay after a gateway restart: %d %s", code, body)
	}
	if code, _ := call("/api/sessions"); code != 404 {
		t.Fatal("the relay must serve /mcp only")
	}
}

func TestChatToolsStdioBridgeReachesTheGateway(t *testing.T) {
	h := newOAuthHarness(t)
	chatFixture(t, h, "session-chat", "")
	next, stop := runTestBridge(t, h.gateway.options.Home, "chat:session-chat", h.server.URL+"/mcp")
	defer stop()
	tools, _ := next()["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["list_sessions"] || !names["send_prompt"] {
		t.Fatalf("bridged tools: %v", names)
	}
	if code, err := RunMCPBridge(t.Context(), MCPBridgeOptions{Home: h.gateway.options.Home, ID: "chat:session-missing", URL: h.server.URL + "/mcp", Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}); code == 0 || err == nil {
		t.Fatal("a chat without a token file got a bridge")
	}
}

func TestHelperReplyEndsAtTheFirstTurn(t *testing.T) {
	entries := []acp.LogEntry{
		{Seq: 1, Kind: "prompt", Payload: json.RawMessage(`{"text":{"text":"Find the bug"}}`)},
		{Seq: 2, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"Looking"}}`)},
		{Seq: 3, Kind: "tool_call", Payload: json.RawMessage(`{"toolCallId":"t1","title":"grep"}`)},
		{Seq: 4, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"_subagent":"c1","text":{"text":"child"}}`)},
		{Seq: 5, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"It is "}}`)},
		{Seq: 6, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"line 4."}}`)},
	}
	if done, reply := helperReply(entries); done || reply != "Looking\n\nIt is line 4." {
		t.Fatalf("running: done=%v reply=%q", done, reply)
	}
	entries = append(entries,
		acp.LogEntry{Seq: 7, Kind: "turn_end", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)},
		acp.LogEntry{Seq: 8, Kind: "agent_message_chunk", Payload: json.RawMessage(`{"text":{"text":"next turn"}}`)},
	)
	if done, reply := helperReply(entries); !done || reply != "Looking\n\nIt is line 4." {
		t.Fatalf("ended: done=%v reply=%q", done, reply)
	}
}
