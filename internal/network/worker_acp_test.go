package network

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
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	base "github.com/olegpustovit/agentpack/internal/harness"
)

type closeableBuffer struct {
	bytes.Buffer
}

func (*closeableBuffer) Close() error { return nil }

type authReplayAgent struct {
	*acptest.ReplayAgent
	authMethods       []sdkacp.AuthMethod
	agentCapabilities sdkacp.AgentCapabilities
	configOptions     []sdkacp.SessionConfigOption
	modes             *sdkacp.SessionModeState
	calls             []string
	configValues      []sdkacp.SetSessionConfigOptionRequest
	forkRequest       *sdkacp.UnstableForkSessionRequest
	forkResponse      sdkacp.UnstableForkSessionResponse
}

func (agent *authReplayAgent) Initialize(context.Context, sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error) {
	agent.calls = append(agent.calls, "initialize")
	return sdkacp.InitializeResponse{
		ProtocolVersion:   sdkacp.ProtocolVersionNumber,
		AgentCapabilities: agent.agentCapabilities,
		AuthMethods:       agent.authMethods,
	}, nil
}

func (agent *authReplayAgent) Authenticate(_ context.Context, request sdkacp.AuthenticateRequest) (sdkacp.AuthenticateResponse, error) {
	agent.calls = append(agent.calls, "authenticate:"+request.MethodId)
	return sdkacp.AuthenticateResponse{}, nil
}

func (agent *authReplayAgent) NewSession(context.Context, sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error) {
	agent.calls = append(agent.calls, "new_session")
	return sdkacp.NewSessionResponse{
		SessionId:     sdkacp.SessionId("test-session"),
		ConfigOptions: agent.configOptions,
		Modes:         agent.modes,
	}, nil
}
func (agent *authReplayAgent) UnstableForkSession(_ context.Context, request sdkacp.UnstableForkSessionRequest) (sdkacp.UnstableForkSessionResponse, error) {
	agent.calls = append(agent.calls, "fork_session")
	agent.forkRequest = &request
	return agent.forkResponse, nil
}
func (agent *authReplayAgent) SetSessionConfigOption(_ context.Context, request sdkacp.SetSessionConfigOptionRequest) (sdkacp.SetSessionConfigOptionResponse, error) {
	agent.configValues = append(agent.configValues, request)
	return sdkacp.SetSessionConfigOptionResponse{}, nil
}

func authReplayConnection(agent *authReplayAgent) (*sdkacp.ClientSideConnection, func()) {
	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	client := acp.NewHarnessClient("", nil, nil, nil)
	clientConn := client.NewClientSideConnection(clientToAgentWriter, agentToClientReader)
	agentConn := sdkacp.NewAgentSideConnection(agent, agentToClientWriter, clientToAgentReader)
	agent.ReplayAgent.SetConnection(agentConn)
	cleanup := func() {
		_ = clientToAgentWriter.Close()
		_ = agentToClientWriter.Close()
		_ = clientToAgentReader.Close()
		_ = agentToClientReader.Close()
	}
	return clientConn, cleanup
}

func TestAdaptAgentStdoutDropsOversizedFrameAndContinues(t *testing.T) {
	input := strings.Repeat("x", maxACPFrameBytes+1) + "\n" + `{"id":1}` + "\n"
	output := &closeableBuffer{}
	log := acp.NewEventLog(0)
	adaptAgentStdout(io.NopCloser(strings.NewReader(input)), output, nil, log)
	if got := output.String(); got != `{"id":1}`+"\n" {
		t.Fatalf("stdout = %q", got)
	}
	if !log.Truncated() {
		t.Fatal("dropped ACP frame did not mark the conversation log truncated")
	}
}
func TestAdaptAgentStdoutPassesThroughAndDropsExplicitEmpty(t *testing.T) {
	log := acp.NewEventLog(0)
	received := make(chan struct{}, 1)
	log.SetOnAppend(func(acp.LogEntry) { received <- struct{}{} })
	client := acp.NewHarnessClient("", nil, log, nil)
	clientInput, clientInputWriter := io.Pipe()
	agentOutput, agentOutputWriter := io.Pipe()
	defer clientInput.Close()
	defer clientInputWriter.Close()
	defer agentOutput.Close()
	defer agentOutputWriter.Close()
	conn := client.NewClientSideConnection(io.Discard, clientInput)
	_ = conn
	adapt := func(line []byte) base.AgentAdaptResult {
		if bytes.Contains(line, []byte(`"text":"drop"`)) {
			return base.AgentAdaptResult{Forward: []byte{}}
		}
		return base.AgentAdaptResult{}
	}
	done := make(chan struct{})
	go func() {
		adaptAgentStdout(agentOutput, clientInputWriter, adapt, log)
		close(done)
	}()
	first := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`)
	if _, err := agentOutputWriter.Write(append(first, '\n')); err != nil {
		t.Fatal(err)
	}
	<-received
	dropped := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"drop"}}}}`)
	if _, err := agentOutputWriter.Write(append(dropped, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = agentOutputWriter.Close()
	<-done
	entries := log.Snapshot()
	if len(entries) != 1 || entries[0].Kind != "agent_message_chunk" || !bytes.Contains(entries[0].Payload, []byte("hello")) {
		t.Fatalf("SDK received %d updates: %+v", len(entries), entries)
	}
}

type stalledACPFrameReader struct {
	input  *strings.Reader
	closed bool
}

func (r *stalledACPFrameReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.input.Len() == 0 {
		return 0, nil
	}
	return r.input.Read(p)
}

func (r *stalledACPFrameReader) Close() error {
	r.closed = true
	return nil
}

func TestAdaptAgentStdoutClosesStalledOversizedFrame(t *testing.T) {
	reader := &stalledACPFrameReader{input: strings.NewReader(strings.Repeat("x", maxACPFrameBytes+1))}
	output := &closeableBuffer{}
	log := acp.NewEventLog(0)
	adaptAgentStdoutWithDrainTimeout(reader, output, nil, log, 0)
	if !reader.closed || !log.Truncated() {
		t.Fatalf("stalled frame closed=%t truncated=%t", reader.closed, log.Truncated())
	}
}

func TestWorkerLaunchContextUsesSafeDefaultPermissions(t *testing.T) {
	dir := t.TempDir()
	ctx, err := workerLaunchContext(WorkerLaunch{
		Directory: dir,
		Session:   SessionInfo{Directory: dir, Harness: "agy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Yolo {
		t.Fatal("ordinary ACP session must not enable yolo permissions")
	}
}

func TestStartACPRequiresHarness(t *testing.T) {
	dir := t.TempDir()
	_, _, err := startACPConversation(WorkerLaunch{
		Transport: "acp",
		Directory: dir,
		Session:   SessionInfo{Directory: dir},
	}, dir)
	if err == nil || !strings.Contains(err.Error(), "harness") {
		t.Fatalf("expected harness error, got %v", err)
	}
}

func TestStartACPReplayAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("replay agent integration")
	}
	dir := t.TempDir()
	source, err := filepath.Abs(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" && !strings.Contains(line, `"kind":"permission_request"`) {
			lines = append(lines, line)
		}
	}
	fixture := filepath.Join(dir, "replay.jsonl")
	if err := os.WriteFile(fixture, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTPACK_TEST_ACP", "2")
	t.Setenv("AGENTPACK_TEST_ACP_FIXTURE", fixture)
	t.Cleanup(func() {
		t.Setenv("AGENTPACK_TEST_ACP", "")
		t.Setenv("AGENTPACK_TEST_ACP_FIXTURE", "")
	})
	session, cleanup, err := startACPConversation(WorkerLaunch{
		Transport: "acp",
		Directory: dir,
		Session:   SessionInfo{Directory: dir, Harness: "claude"},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if session.agentSessionID == "" {
		t.Fatal("ACP session should expose its native session ID for safe resume")
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Prompt(ctx, acp.PromptInput{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := session.Attach(0)
	if len(entries) == 0 {
		t.Fatal("expected replay log entries after prompt")
	}
	if entries[len(entries)-1].Kind != "turn_end" {
		t.Fatalf("last event = %q, want turn_end", entries[len(entries)-1].Kind)
	}
	if state := session.ConversationState(); state != "idle" {
		t.Fatalf("conversation state after turn = %q, want idle", state)
	}
}

func TestACPNeedsYouPersistsWithoutConversationReader(t *testing.T) {
	sessionDir := t.TempDir()
	record := WorkerRecord{ID: "session-test", Session: SessionInfo{Transport: "acp", ConversationState: "idle"}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "worker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(0)
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	conversation.SetStateChange("idle", func(state string) {
		if err := patchWorkerConversationState(sessionDir, state); err != nil {
			t.Error(err)
		}
	})
	log.Append("permission_request", 1, json.RawMessage(`{"requestId":"p1"}`))
	data, err = os.ReadFile(filepath.Join(sessionDir, "worker.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Session.ConversationState != "needs_you" {
		t.Fatalf("worker state without a reader = %q", record.Session.ConversationState)
	}
}
func TestConversationStateWritesOnlyOnLabelTransitions(t *testing.T) {
	log := acp.NewEventLog(0)
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	var writes []string
	conversation.SetStateChange("idle", func(state string) { writes = append(writes, state) })
	log.Append("prompt", 1, json.RawMessage(`{}`))
	for i := range 1_000 {
		log.Append("agent_message_chunk", int64(i+2), json.RawMessage(`{"text":{"text":"x"}}`))
	}
	if len(writes) != 1 || writes[0] != "working" {
		t.Fatalf("state writes across streamed chunks = %v", writes)
	}
	log.Append("turn_end", 2_000, json.RawMessage(`{}`))
	if len(writes) != 2 || writes[1] != "idle" {
		t.Fatalf("state writes after turn end = %v", writes)
	}
}
func TestConversationStateSeedsFromRetainedEvents(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{}`))
	log.Append("permission_request", 2, json.RawMessage(`{"requestId":"p1"}`))
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	if got := conversation.ConversationState(); got != "needs_you" {
		t.Fatalf("seeded conversation state = %q", got)
	}
	log.Append("permission_result", 3, json.RawMessage(`{"requestId":"p1","optionId":"allow"}`))
	if got := conversation.ConversationState(); got != "working" {
		t.Fatalf("state after seeded permission answer = %q", got)
	}
}

func TestACPAgentExitEndsWorkingState(t *testing.T) {
	log := acp.NewEventLog(0)
	conversation := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	log.Append("prompt", 1, json.RawMessage(`{}`))
	if got := conversation.ConversationState(); got != "working" {
		t.Fatalf("state before exit = %q", got)
	}
	appendACPAgentExit(log)
	entries, _ := conversation.Attach(0)
	if entries[len(entries)-1].Kind != "agent_exit" || conversation.ConversationState() != "idle" {
		t.Fatalf("agent exit did not end the turn: entries=%+v state=%q", entries, conversation.ConversationState())
	}
}

func TestSessionInfoAgentCapabilitiesRoundTripUsesAllowlist(t *testing.T) {
	capabilities := agentCapabilitiesView(sdkacp.AgentCapabilities{
		LoadSession: true,
		Meta:        map[string]any{"private": "secret"},
		PromptCapabilities: sdkacp.PromptCapabilities{
			Image: true,
			Audio: true,
		},
		SessionCapabilities: sdkacp.SessionCapabilities{
			Resume: &sdkacp.SessionResumeCapabilities{},
			Fork:   &sdkacp.SessionForkCapabilities{},
			List:   &sdkacp.SessionListCapabilities{},
		},
	})
	data, err := json.Marshal(SessionInfo{AgentCapabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"agent_capabilities":`) ||
		strings.Contains(string(data), "private") || strings.Contains(string(data), "audio") || strings.Contains(string(data), `"list"`) {
		t.Fatalf("capability view contains unapproved fields: %s", data)
	}
	var got SessionInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.AgentCapabilities == nil || !got.AgentCapabilities.LoadSession ||
		got.AgentCapabilities.PromptCapabilities == nil || !got.AgentCapabilities.PromptCapabilities.Image ||
		got.AgentCapabilities.SessionCapabilities == nil ||
		got.AgentCapabilities.SessionCapabilities.Resume == nil ||
		got.AgentCapabilities.SessionCapabilities.Fork == nil {
		t.Fatalf("round-tripped capabilities = %+v", got.AgentCapabilities)
	}
	var old SessionInfo
	if err := json.Unmarshal([]byte(`{"id":"session-old"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.AgentCapabilities != nil {
		t.Fatalf("legacy record acquired capabilities: %+v", old.AgentCapabilities)
	}
}

func TestConversationConfigJSONPreservesOnlyACPSetupFields(t *testing.T) {
	conversation := NewConversationSession(nil, acp.NewBroker(nil, nil, nil, nil))
	conversation.SetConfigOptions(json.RawMessage(`[{"id":"model","category":"model","type":"select","currentValue":"m1","options":[{"value":"m1","name":"Model 1"}]}]`))
	conversation.SetModes(json.RawMessage(`{"currentModeId":"ask","availableModes":[{"id":"ask","name":"Ask"}]}`))
	var got map[string]json.RawMessage
	if err := json.Unmarshal(conversation.ConfigJSON(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !json.Valid(got["configOptions"]) || !json.Valid(got["modes"]) {
		t.Fatalf("config response = %s", conversation.ConfigJSON())
	}
	absent := NewConversationSession(nil, acp.NewBroker(nil, nil, nil, nil)).ConfigJSON()
	if string(absent) != `{}` {
		t.Fatalf("absent ACP setup fields = %s", absent)
	}
}

func TestACPConfigOptionEncodingPreservesBooleanAndString(t *testing.T) {
	var received []string
	broker := acp.NewBroker(nil, nil, nil, func(_ context.Context, _ string, value string) error {
		received = append(received, value)
		return nil
	})
	conversation := NewConversationSession(nil, broker)
	if err := conversation.SetConfigOption(context.Background(), "enabled", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	if err := conversation.SetConfigOption(context.Background(), "model", json.RawMessage(`"true"`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(received, "|") != `true|"true"` {
		t.Fatalf("config values encoded for ACP = %q", received)
	}
}

func TestBrokerSetConfigOptionForwardsBooleanAndStringVariants(t *testing.T) {
	agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil)}
	client, cleanup := authReplayConnection(agent)
	defer cleanup()
	conn := client
	sessionID := sdkacp.SessionId("session")
	var mu sync.Mutex
	setConfig := brokerSetConfigOption(&mu, &conn, &sessionID)
	if err := setConfig(context.Background(), "enabled", `true`); err != nil {
		t.Fatal(err)
	}
	if err := setConfig(context.Background(), "model", `"true"`); err != nil {
		t.Fatal(err)
	}
	if len(agent.configValues) != 2 || agent.configValues[0].Boolean == nil || !agent.configValues[0].Boolean.Value ||
		agent.configValues[1].ValueId == nil || string(agent.configValues[1].ValueId.Value) != "true" {
		t.Fatalf("ACP config requests = %+v", agent.configValues)
	}
}

func TestRawSessionNoticeAndCompactionUpdatesReachEventLog(t *testing.T) {
	updates := []struct {
		kind    string
		payload string
	}{
		{
			kind:    "notice",
			payload: `{"sessionUpdate":"notice","severity":"warning","title":"MCP server unavailable","description":"Continuing without it."}`,
		},
		{
			kind:    "compaction_update",
			payload: `{"sessionUpdate":"compaction_update","compactionId":"cmp_1","status":"completed","summary":[{"type":"text","text":"Retained context"}]}`,
		},
		{
			kind:    "compaction_summary_chunk",
			payload: `{"sessionUpdate":"compaction_summary_chunk","compactionId":"cmp_1","content":{"type":"text","text":"Retained context"}}`,
		},
	}
	log := acp.NewEventLog(0)
	for _, update := range updates {
		line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":` + update.payload + `}}`)
		appendRawSessionUpdate(log, line)
	}
	entries := log.Snapshot()
	if len(entries) != len(updates) {
		t.Fatalf("raw update events = %+v", entries)
	}
	for i, entry := range entries {
		if entry.Kind != updates[i].kind || string(entry.Payload) != updates[i].payload {
			t.Fatalf("raw event %d = %q %s, want %q %s", i, entry.Kind, entry.Payload, updates[i].kind, updates[i].payload)
		}
	}
}

func TestInitializeACPSessionAuthenticatesBeforeNewSession(t *testing.T) {
	agent := &authReplayAgent{
		ReplayAgent: acptest.NewReplayAgent(nil),
		authMethods: []sdkacp.AuthMethod{{Agent: &sdkacp.AuthMethodAgent{Id: "grok.com", Name: "Grok"}}},
		agentCapabilities: sdkacp.AgentCapabilities{
			LoadSession:        true,
			Meta:               map[string]any{"private": "secret"},
			PromptCapabilities: sdkacp.PromptCapabilities{Image: true, Audio: true},
			SessionCapabilities: sdkacp.SessionCapabilities{
				Resume: &sdkacp.SessionResumeCapabilities{},
				Fork:   &sdkacp.SessionForkCapabilities{},
				List:   &sdkacp.SessionListCapabilities{},
			},
		},
		configOptions: []sdkacp.SessionConfigOption{},
		modes: &sdkacp.SessionModeState{
			CurrentModeId:  sdkacp.SessionModeId("ask"),
			AvailableModes: []sdkacp.SessionMode{{Id: sdkacp.SessionModeId("ask"), Name: "Ask"}},
		},
	}
	clientConn, cleanup := authReplayConnection(agent)
	defer cleanup()
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(agent.calls, ",") != "initialize,authenticate:grok.com,new_session" {
		t.Fatalf("ACP request order = %v", agent.calls)
	}
	if setup.AgentSessionID != "test-session" || setup.ConfigOptions != nil {
		t.Fatalf("initial session setup = %+v", setup)
	}
	if setup.AgentCapabilities == nil || !setup.AgentCapabilities.LoadSession ||
		setup.AgentCapabilities.PromptCapabilities == nil || !setup.AgentCapabilities.PromptCapabilities.Image ||
		setup.AgentCapabilities.SessionCapabilities == nil ||
		setup.AgentCapabilities.SessionCapabilities.Resume == nil ||
		setup.AgentCapabilities.SessionCapabilities.Fork == nil {
		t.Fatalf("sanitized agent capabilities = %+v", setup.AgentCapabilities)
	}
	serializedCapabilities, err := json.Marshal(setup.AgentCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serializedCapabilities), "private") ||
		strings.Contains(string(serializedCapabilities), "audio") ||
		strings.Contains(string(serializedCapabilities), `"list"`) {
		t.Fatalf("agent capability view leaked unrelated fields: %s", serializedCapabilities)
	}
	var modes sdkacp.SessionModeState
	if err := json.Unmarshal(setup.Modes, &modes); err != nil {
		t.Fatal(err)
	}
	if string(modes.CurrentModeId) != "ask" || len(modes.AvailableModes) != 1 || modes.AvailableModes[0].Name != "Ask" {
		t.Fatalf("initial ACP modes = %+v", modes)
	}
}
func TestInitializeACPSessionForksWhenAdvertised(t *testing.T) {
	agent := &authReplayAgent{
		ReplayAgent: acptest.NewReplayAgent(nil),
		agentCapabilities: sdkacp.AgentCapabilities{
			SessionCapabilities: sdkacp.SessionCapabilities{Fork: &sdkacp.SessionForkCapabilities{}},
		},
		forkResponse: sdkacp.UnstableForkSessionResponse{
			SessionId: sdkacp.SessionId("forked-session"),
			ConfigOptions: []sdkacp.UnstableSessionConfigOption{
				{Boolean: &sdkacp.UnstableSessionConfigOptionBoolean{
					Id: sdkacp.SessionConfigId("thinking"), Name: "Thinking", Type: "boolean", CurrentValue: true,
				}},
			},
			Modes: &sdkacp.SessionModeState{CurrentModeId: sdkacp.SessionModeId("ask")},
		},
	}
	clientConn, cleanup := authReplayConnection(agent)
	defer cleanup()
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(agent.calls, ",") != "initialize,fork_session" {
		t.Fatalf("ACP request order = %v", agent.calls)
	}
	if agent.forkRequest == nil || agent.forkRequest.SessionId != sdkacp.SessionId("source-session") ||
		agent.forkRequest.Cwd == "" || len(agent.forkRequest.McpServers) != 0 || len(agent.forkRequest.AdditionalDirectories) != 0 {
		t.Fatalf("fork request = %+v", agent.forkRequest)
	}
	if setup.AgentSessionID != "forked-session" || setup.AgentSessionID == string(agent.forkRequest.SessionId) ||
		string(setup.ConfigOptions) != `[{"currentValue":true,"id":"thinking","name":"Thinking","type":"boolean"}]` {
		t.Fatalf("forked session setup = %+v", setup)
	}
}

func TestInitializeACPSessionRejectsForkWithoutCapability(t *testing.T) {
	agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil)}
	clientConn, cleanup := authReplayConnection(agent)
	defer cleanup()
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "")
	if err == nil || !strings.Contains(err.Error(), "does not advertise session.fork") {
		t.Fatalf("unadvertised fork error = %v", err)
	}
	if strings.Join(agent.calls, ",") != "initialize" || agent.forkRequest != nil {
		t.Fatalf("requests after unsupported fork = %v", agent.calls)
	}
}

func TestMarshalConfigOptionsPreservesAbsentAndEmpty(t *testing.T) {
	if marshalConfigOptions(nil) != nil {
		t.Fatal("absent configOptions should stay omitted")
	}
	if got := string(marshalConfigOptions([]sdkacp.SessionConfigOption{})); got != "[]" {
		t.Fatalf("empty configOptions = %s", got)
	}
}

func TestInitializeACPSessionRejectsUnadvertisedAuthMethod(t *testing.T) {
	agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil)}
	clientConn, cleanup := authReplayConnection(agent)
	defer cleanup()
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com")
	if err == nil || !strings.Contains(err.Error(), "was not advertised") {
		t.Fatalf("unadvertised auth method error = %v", err)
	}
	if strings.Join(agent.calls, ",") != "initialize" {
		t.Fatalf("requests after unsupported auth method = %v", agent.calls)
	}
}
func TestRunWorkerACPRestartAndStop(t *testing.T) {
	directory := t.TempDir()
	home := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "acp", "full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTPACK_TEST_ACP", "1")
	t.Setenv("AGENTPACK_TEST_ACP_FIXTURE", fixture)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := RunWorker(context.Background(), home, WorkerLaunch{
		Session:   SessionInfo{ID: "test-session", Directory: directory, Harness: "claude", Mode: "default"},
		Program:   program,
		Directory: directory,
		Transport: "acp",
	}, func(id string) error {
		if err := RestartWorker(ctx, home, id, "restart-operation-0001"); err != nil {
			return err
		}
		return StopWorker(ctx, home, id)
	})
	if err != nil || code != 0 {
		t.Fatalf("RunWorker() = %d, %v", code, err)
	}
}
