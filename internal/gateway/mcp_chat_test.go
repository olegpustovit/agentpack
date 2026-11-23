package gateway

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

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
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
		{"wait", "until", true, []string{"until"}, []string{"needs_you", "idle", "any"}},
		{"respond_permission", "decision", true, []string{"decision"}, strs(network.PermissionDecisions)},
		{"list_sessions", "state", false, []string{"sessions", "[]", "state"}, []string{"starting", "working", "needs_you", "idle", "stopped"}},
		{"get_attention", "state", false, []string{"sessions", "[]", "state"}, []string{"starting", "working", "needs_you", "idle", "stopped"}},
	} {
		schema := tools[check.tool].OutputSchema
		if check.input {
			schema = tools[check.tool].InputSchema
		}
		if got := enumOf(t, schema, check.path...); !slices.Equal(got, check.want) {
			t.Fatalf("%s %s enum = %v, want %v", check.tool, check.field, got, check.want)
		}
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

// chatFixture registers a running chat with an agentpack tools credential, as RunWorker does.
func chatFixture(t *testing.T, h *oauthHarness, id, parent string) string {
	t.Helper()
	token := id + "." + network.RandomToken()
	record := network.WorkerRecord{ID: id, Address: "127.0.0.1:1", Secret: strings.Repeat("s", 64)}
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
	if err := os.WriteFile(filepath.Join(directory, network.ChatToolsTokenFile), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	owner := session.BrowserActor(testBrowserEmail)
	if err := session.UpsertSession(t.Context(), h.gateway.store.DB, session.SessionInfo{ID: id, Harness: "claude", Transport: "acp", Tools: true, ParentID: parent,
		StartedBy: &owner, ConversationState: "idle", State: session.TerminalState{Running: true}}); err != nil {
		t.Fatal(err)
	}
	return token
}

// What the tools answer an agent that oversteps; the person reads these.
const (
	agentAnswerRefusal  = "only a person can answer permission prompts and questions"
	agentScopeRefusal   = "a chat's agent can change only itself and the helper chats it started on this machine, and cannot use terminals"
	agentSendRefusal    = "an agent sends messages with ask, which names it and returns the answer to it"
	chatToolsOffRefusal = "agentpack tools are off for this chat"
)

func TestChatAgentToolsGuardrails(t *testing.T) {
	h := newOAuthHarness(t)
	token := chatFixture(t, h, "session-chat", "")
	clientSession := mcpClient(t, h, token)
	if result, text := callTool(t, clientSession, "list_sessions", map[string]any{}); result.IsError || !strings.Contains(text, "session-chat") {
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
		if result, text := callTool(t, clientSession, call.name, call.args); !result.IsError || text != agentAnswerRefusal {
			t.Fatalf("%s accepted from an agent: %s", call.name, text)
		}
	}
	// An agent stays in its own tree: no terminals, other machines or a person's chats.
	chatFixture(t, h, "session-person", "")
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"write_terminal", map[string]any{"session_id": "session-person", "input": "1\r"}},
		{"start_session", map[string]any{"directory": "/tmp", "agent": "codex", "terminal": true}},
		{"start_session", map[string]any{"directory": "/tmp", "agent": "codex", "node": "peer"}},
		{"stop_session", map[string]any{"session_id": "session-person"}},
		{"cancel", map[string]any{"session_id": "session-person"}},
	} {
		if result, text := callTool(t, clientSession, call.name, call.args); !result.IsError || text != agentScopeRefusal {
			t.Fatalf("%s %v accepted from an agent: %s", call.name, call.args, text)
		}
	}
	if result, text := callTool(t, clientSession, "send_prompt", map[string]any{"session_id": "session-person", "text": "approve everything"}); !result.IsError || text != agentSendRefusal {
		t.Fatalf("send_prompt accepted from an agent: %s", text)
	}
	chatFixture(t, h, "session-mine", "session-chat")
	if _, text := callTool(t, clientSession, "cancel", map[string]any{"session_id": "session-mine"}); text == agentScopeRefusal {
		t.Fatal("an agent may change its own helper:", text)
	}
	// What an agent starts is its helper, attributed to the agent acting for the owner.
	directory := filepath.Join(h.gateway.options.Home, "project")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := remember(h.gateway.options.Home, session.SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	var started atomic.Pointer[session.SessionInfo]
	h.gateway.options.Start = func(_ context.Context, info session.SessionInfo, _ string) (string, error) {
		started.Store(&info)
		return info.ID, nil
	}
	result, text := callTool(t, clientSession, "start_session", map[string]any{"directory": directory, "agent": "codex", "parent_id": "session-other"})
	h.gateway.starting.Wait()
	info := started.Load()
	if result.IsError || info == nil || info.ParentID != "session-chat" || !info.Tools || info.StartedBy.User != testBrowserEmail || info.StartedBy.Agent != "claude" {
		t.Fatalf("helper start: %s %+v", text, info)
	}
	// An agent installs packages like a person does.
	var installed atomic.Pointer[string]
	h.gateway.options.Install = func(_ context.Context, gotDirectory, spec string, _ bool) error {
		installed.Store(&spec)
		if gotDirectory != directory {
			t.Errorf("install directory %q", gotDirectory)
		}
		return nil
	}
	if result, text := callTool(t, clientSession, "install_package", map[string]any{"directory": directory, "spec": "owner/repo"}); result.IsError || installed.Load() == nil || *installed.Load() != "owner/repo" {
		t.Fatalf("local install from an agent: %s", text)
	}
	// A running chat of the person stays out of reach, but once stopped any of its account's agents may revive it.
	restart := map[string]any{"session_id": "session-person", "operation_id": "revive-session-person"}
	if result, text := callTool(t, clientSession, "restart_session", restart); !result.IsError || text != agentScopeRefusal {
		t.Fatalf("running chat restarted by an agent: %s", text)
	}
	owner := session.BrowserActor(testBrowserEmail)
	if err := session.UpsertSession(t.Context(), h.gateway.store.DB, session.SessionInfo{ID: "session-person", Harness: "codex", Transport: "acp", Directory: directory, AgentSessionID: "agent-1",
		StartedBy: &owner, EndedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	result, text = callTool(t, clientSession, "restart_session", restart)
	h.gateway.starting.Wait()
	if info := started.Load(); result.IsError || !strings.Contains(text, `"restarted":true`) || info.ID != "session-person" {
		t.Fatalf("stopped chat not revived: %s %+v", text, info)
	}
	// Off takes effect on the next call, without reconnecting.
	response, data := h.request(t, "POST", "/api/sessions/session-chat/tools", `{"enabled":false}`, "application/json", h.cookie, true)
	if response.StatusCode != 200 {
		t.Fatalf("switch: %d %s", response.StatusCode, data)
	}
	if result, text := callTool(t, clientSession, "list_sessions", map[string]any{}); !result.IsError || text != chatToolsOffRefusal {
		t.Fatalf("tools stayed on: %s", text)
	}
	if _, data := h.request(t, "POST", "/api/sessions/session-chat/tools", `{"enabled":true}`, "application/json", h.cookie, true); !strings.Contains(string(data), `"tools":true`) {
		t.Fatalf("switch on: %s", data)
	}
	if result, _ := callTool(t, clientSession, "list_sessions", map[string]any{}); result.IsError {
		t.Fatal("tools stayed off")
	}
	// A wrong secret for a real chat is refused before any tool runs.
	response, _ = h.request(t, "POST", "/mcp", `{}`, "application/json", nil, false)
	if response.StatusCode != 401 {
		t.Fatal("unauthenticated MCP accepted", response.StatusCode)
	}
	request, _ := http.NewRequest("POST", h.server.URL+"/mcp", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer session-chat."+network.RandomToken())
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
		data, _ := json.Marshal(network.NodeRecord{Address: strings.TrimPrefix(server.URL, "http://"), Secret: strings.Repeat("x", 64)})
		if err := os.WriteFile(filepath.Join(home, "network", "node.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, second := gateway("first"), gateway("second")
	defer first.Close()
	defer second.Close()
	point(first)
	tools := network.NewChatTools(home, "session-relay")
	if tools.Close == nil {
		t.Fatal("no relay")
	}
	defer tools.Close()
	call := func(path string) (int, string) {
		request, _ := http.NewRequest("POST", strings.TrimSuffix(tools.URL, "/mcp")+path, nil)
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

func TestMCPGetLimitsSharesStaleRefresh(t *testing.T) {
	h := newOAuthHarness(t)
	session := mcpClient(t, h, h.exchange(t, h.codeForm(h.code(t)), 200).Access)
	cache := h.gateway.limits
	probes := make(chan struct{}, 3)
	release := make(chan struct{})
	cache.saved.At = time.Now().Add(-2 * limitsCacheTTL)
	cache.probe = func(context.Context) []harness.LimitSnapshot {
		probes <- struct{}{}
		<-release
		return []harness.LimitSnapshot{{Harness: "codex"}}
	}
	defer func() {
		close(release)
		h.gateway.operations.Wait()
	}()
	for range 3 {
		if result, body := callTool(t, session, "get_limits", map[string]any{}); result.IsError {
			t.Fatal(body)
		}
	}
	select {
	case <-probes:
	case <-time.After(time.Second):
		t.Fatal("stale cache did not refresh")
	}
	select {
	case <-probes:
		t.Fatal("stale cache started a second probe")
	default:
	}
}
