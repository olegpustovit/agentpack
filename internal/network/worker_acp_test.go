package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	newRequest        sdkacp.NewSessionRequest
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

func (agent *authReplayAgent) NewSession(_ context.Context, request sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error) {
	agent.calls = append(agent.calls, "new_session")
	agent.newRequest = request
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
	client := acp.NewHarnessClient("", nil, nil)
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
	client := acp.NewHarnessClient("", log, nil)
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
	_, _, err := startACPConversation(context.Background(), WorkerLaunch{
		Transport: "acp",
		Directory: dir,
		Session:   SessionInfo{Directory: dir},
	}, dir, NewConversationSession(acp.NewEventLog(0), nil))
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
		// Both block until a viewer answers, and this test attaches none.
		if line != "" && !strings.Contains(line, `"kind":"permission_request"`) && !strings.Contains(line, `"kind":"elicitation"`) {
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
	session, cleanup, err := startACPConversation(context.Background(), WorkerLaunch{
		Transport: "acp",
		Directory: dir,
		Session:   SessionInfo{Directory: dir, Harness: "claude"},
	}, dir, NewConversationSession(acp.NewEventLog(0), nil))
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
	entries, _, _ := session.Attach(0)
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
	store := testSessionStore(t, t.TempDir())
	ctx := context.Background()
	info := SessionInfo{ID: "session-test", Transport: "acp", ConversationState: "idle"}
	if err := upsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	log, err := newConversationLog(ctx, store, info.ID, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	log.Append("permission_request", 1, json.RawMessage(`{"requestId":"p1"}`))
	got, err := storedSession(ctx, store.Reader, info.ID)
	if err != nil || got.ConversationState != "needs_you" {
		t.Fatalf("state without a reader=%q err=%v", got.ConversationState, err)
	}
}

// A state change is stored with the events behind it before anything acts on it; streamed
// chunks in between are left to the batching writer.
func TestConversationStateIsStoredOnTransitions(t *testing.T) {
	log := acp.NewEventLog(0)
	NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	var mu sync.Mutex
	var states []string
	if err := log.EnableSink(func(changes acp.LogChanges) error {
		var fold conversationFold
		if err := json.Unmarshal(changes.Checkpoint, &fold); err != nil {
			return err
		}
		mu.Lock()
		states = append(states, fold.derived().State)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return states[len(states)-1]
	}
	log.Append("prompt", 1, json.RawMessage(`{}`))
	if last() != "working" {
		t.Fatalf("stored states after prompt = %v", states)
	}
	for i := range 1_000 {
		log.Append("agent_message_chunk", int64(i+2), json.RawMessage(`{"text":{"text":"x"}}`))
	}
	log.Append("turn_end", 2_000, json.RawMessage(`{}`))
	if last() != "idle" {
		t.Fatalf("stored states after turn end = %v", states)
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
	entries, _, _ := conversation.Attach(0)
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
	setConfig := brokerSetConfigOption(&mu, &conn, &sessionID, nil)
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
		acp.AppendRawSessionUpdate(log, line)
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

func TestInitializeACPWaitsForStagingBeforeCreatingSession(t *testing.T) {
	agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil)}
	conn, cleanup := authReplayConnection(agent)
	defer cleanup()
	stageErr := errors.New("staging rejected")
	_, err := initializeACPSession(t.Context(), conn, t.TempDir(), "", "", "", chatTools{}, func(context.Context) error {
		if strings.Join(agent.calls, ",") != "initialize" {
			t.Fatalf("ACP calls before verified staging = %v", agent.calls)
		}
		return stageErr
	})
	if !errors.Is(err, stageErr) || strings.Join(agent.calls, ",") != "initialize" {
		t.Fatalf("failed staging allowed a session request: %v, %v", agent.calls, err)
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
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com", chatTools{}, nil)
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
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "", chatTools{}, nil)
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
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "", chatTools{}, nil)
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
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com", chatTools{}, nil)
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
	code, err := RunWorker(context.Background(), home, "test", WorkerLaunch{
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
	events := workerDiagnosticEvents(t, home)
	names := map[string]int{}
	for _, event := range events {
		names[event.Name]++
		if event.SessionID == "" || event.Proc != "worker" {
			t.Fatalf("ACP worker event identity %+v", event)
		}
	}
	if names["start"] != 1 || names["restart"] != 1 || names["stop"] != 1 || len(events) != 3 {
		t.Fatalf("ACP lifecycle events %+v", names)
	}
}

func TestFailedPromptEndsTheTurnWithANotice(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{"text":"hi"}`))
	logFailedTurn(log, "claude", sdkacp.NewAuthRequired(nil), true)
	entries := log.Snapshot()
	state := acp.ReduceLog(entries)
	if state.Working || len(entries) != 3 || entries[1].Kind != "notice" || !strings.Contains(string(entries[1].Payload), `"code":"auth_required"`) || !strings.Contains(string(entries[1].Payload), `"id":"claudeai"`) {
		t.Fatalf("working=%v entries=%v", state.Working, entries)
	}
}

func TestStartPromptOutlivesTheRequest(t *testing.T) {
	previous := promptAcceptWait
	promptAcceptWait = 10 * time.Millisecond
	t.Cleanup(func() { promptAcceptWait = previous })
	release, finished := make(chan struct{}), make(chan error, 1)
	session := NewConversationSession(acp.NewEventLog(0), acp.NewBroker(func(ctx context.Context, _ acp.PromptInput) error {
		<-release
		finished <- ctx.Err()
		return nil
	}, nil, nil, nil))
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := startPrompt(ctx, session, acp.PromptInput{Text: "go"}, false); err != nil {
		t.Fatalf("a long turn was not accepted: %v", err)
	}
	cancel()
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("ending the request cancelled the turn: %v", err)
	}
	if _, err := startPrompt(t.Context(), NewConversationSession(acp.NewEventLog(0), acp.NewBroker(func(context.Context, acp.PromptInput) error {
		return errors.New("refused")
	}, nil, nil, nil)), acp.PromptInput{Text: "go"}, false); err == nil {
		t.Fatal("an immediate failure was reported as accepted")
	}
}

func TestStartPromptReturnsOnceThePromptIsSent(t *testing.T) {
	previous := promptAcceptWait
	promptAcceptWait = time.Hour
	t.Cleanup(func() { promptAcceptWait = previous })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	session := NewConversationSession(acp.NewEventLog(0), acp.NewBroker(func(ctx context.Context, _ acp.PromptInput) error {
		acp.Accepted(ctx)
		<-release
		return errors.New("late failure is logged, not returned")
	}, nil, nil, nil))
	done := make(chan error, 1)
	go func() {
		_, err := startPrompt(t.Context(), session, acp.PromptInput{Text: "go"}, false)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt request waited for the turn instead of acceptance")
	}
}

func TestConversationStreamPushesAppendsWithoutPolling(t *testing.T) {
	log := acp.NewEventLog(0)
	log.Append("prompt", 1, json.RawMessage(`{"text":"old"}`))
	conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(&WorkerHTTP{}).serveConversationSSE(w, r, conv)
	}))
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL + "?since=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if text := scanner.Text(); strings.HasPrefix(text, "data:") {
				lines <- text
			}
		}
		close(lines)
	}()
	started := time.Now()
	log.Append("agent_message_chunk", 2, json.RawMessage(`{"text":"new"}`))
	select {
	case line := <-lines:
		if !strings.Contains(line, `"seq":2`) {
			t.Fatalf("since was not honored: %s", line)
		}
		if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
			t.Fatalf("append took %s to reach the stream", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("append never reached the stream")
	}
}

func TestACPAuthRequiredNamesAgentAndMachine(t *testing.T) {
	cause := fmt.Errorf("acp session/new: %w", sdkacp.NewAuthRequired(nil))
	err := explainACPAuthRequired(cause, base.Agy)
	if !strings.HasPrefix(err.Error(), "Antigravity"+notSignedIn) || strings.Contains(err.Error(), "acp") {
		t.Fatalf("auth-required error not explained: %v", err)
	}
	other := errors.New("acp session/new: boom")
	if explainACPAuthRequired(other, base.Agy) != other {
		t.Fatal("unrelated error rewritten")
	}
}

// heldAgent answers each prompt only when the test releases one, and records what it was sent.
type heldAgent struct {
	*acptest.ReplayAgent
	received chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	methods  []string
	steering string // the _session/steering outcome
}

func (agent *heldAgent) Prompt(context.Context, sdkacp.PromptRequest) (sdkacp.PromptResponse, error) {
	agent.record("session/prompt")
	agent.received <- struct{}{}
	<-agent.release
	return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
}

func (agent *heldAgent) Cancel(context.Context, sdkacp.CancelNotification) error {
	agent.record("session/cancel")
	return nil
}

func (agent *heldAgent) HandleExtensionMethod(_ context.Context, method string, _ json.RawMessage) (any, error) {
	agent.record(method)
	return map[string]string{"outcome": agent.steering}, nil
}

func (agent *heldAgent) record(method string) {
	agent.mu.Lock()
	agent.methods = append(agent.methods, method)
	agent.mu.Unlock()
}

func newHeldPrompter(t *testing.T) (*acpPrompter, *heldAgent) {
	t.Helper()
	agent := &heldAgent{ReplayAgent: acptest.NewReplayAgent(nil), received: make(chan struct{}, 4), release: make(chan struct{}), steering: "injected"}
	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	log := acp.NewEventLog(0)
	clientConn := acp.NewHarnessClient("", log, nil).NewClientSideConnection(clientToAgentWriter, agentToClientReader)
	agent.SetConnection(sdkacp.NewAgentSideConnection(agent, agentToClientWriter, clientToAgentReader))
	t.Cleanup(func() {
		for _, pipe := range []io.Closer{clientToAgentWriter, agentToClientWriter, clientToAgentReader, agentToClientReader} {
			_ = pipe.Close()
		}
	})
	return &acpPrompter{log: log, conn: clientConn, sessID: "s"}, agent
}

func logEntryKinds(log *acp.EventLog) string {
	var kinds []string
	for _, entry := range log.Snapshot() {
		kinds = append(kinds, entry.Kind)
	}
	return strings.Join(kinds, ",")
}

func TestPromptSentDuringATurnIsDeliveredUntilTheAgentTakesItIn(t *testing.T) {
	prompter, agent := newHeldPrompter(t)
	errs := make(chan error, 2)
	for _, input := range []acp.PromptInput{{Text: "goal"}, {Text: "message", QueueID: "q1"}} {
		go func() { errs <- prompter.prompt(context.Background(), input) }()
		<-agent.received
	}
	entries := prompter.log.Snapshot()
	if got := logEntryKinds(prompter.log); got != "prompt,queued_prompt" || !strings.Contains(string(entries[1].Payload), `"delivered":true`) {
		t.Fatalf("while the first turn runs: %s %s", got, entries[len(entries)-1].Payload)
	}
	// claude-agent-acp answers the running prompt when the next one's echo takes the turn over.
	agent.release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if got := logEntryKinds(prompter.log); got != "prompt,queued_prompt,prompt" {
		t.Fatalf("after the hand-off: %s", got)
	}
	agent.release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	entries = prompter.log.Snapshot()
	if got := logEntryKinds(prompter.log); got != "prompt,queued_prompt,prompt,turn_end" || !strings.Contains(string(entries[2].Payload), `"queueId":"q1"`) {
		t.Fatalf("log = %s; the inserted prompt must carry its queue ID", got)
	}
}

func TestSteerJoinsTheTurnWithoutCancel(t *testing.T) {
	prompter, agent := newHeldPrompter(t)
	injected, err := prompter.steer(context.Background(), acp.PromptInput{Text: "look here", QueueID: "q1"})
	if err != nil || !injected {
		t.Fatalf("steer = %t, %v", injected, err)
	}
	agent.steering = "promptRequired"
	if injected, err := prompter.steer(context.Background(), acp.PromptInput{Text: "later", QueueID: "q2"}); err != nil || injected {
		t.Fatalf("steer with no running turn = %t, %v", injected, err)
	}
	agent.mu.Lock()
	methods := strings.Join(agent.methods, ",")
	agent.mu.Unlock()
	if methods != "_session/steering,_session/steering" {
		t.Fatalf("agent received %s; a steer must never send session/cancel", methods)
	}
	entries := prompter.log.Snapshot()
	if len(entries) != 1 || entries[0].Kind != "prompt" || !strings.Contains(string(entries[0].Payload), `"queueId":"q1"`) {
		t.Fatalf("only the injected steer is a prompt entry: %s", logEntryKinds(prompter.log))
	}
}

// P3.6: updates the SDK decodes, raw updates it cannot and adapter log entries reach the log in the
// order the agent wrote them, however far the SDK's handlers lag behind.
func TestAgentStdoutIsLoggedInWireOrder(t *testing.T) {
	log := acp.NewEventLog(0)
	client := acp.NewHarnessClient("", log, nil)
	clientInput, clientInputWriter := io.Pipe()
	agentOutput, agentOutputWriter := io.Pipe()
	defer clientInput.Close()
	conn := client.NewClientSideConnection(io.Discard, clientInput)
	adapt := func(line []byte) base.AgentAdaptResult {
		if bytes.Contains(line, []byte(`"text":"adapted"`)) {
			return base.AgentAdaptResult{Log: []base.LogKind{{Kind: "tool_call", Payload: json.RawMessage(`{"toolCallId":"grouped"}`)}}}
		}
		return base.AgentAdaptResult{}
	}
	done := make(chan struct{})
	go func() {
		adaptAgentStdout(agentOutput, clientInputWriter, adapt, log)
		close(done)
	}()
	chunk := func(text string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + text + `"}}}}`
	}
	notice := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"notice","severity":"warning","title":"t"}}}`
	var want []string
	var input strings.Builder
	for round := range 20 {
		for i := range 30 {
			input.WriteString(chunk(fmt.Sprintf("r%dc%d", round, i)) + "\n")
			want = append(want, "agent_message_chunk")
		}
		input.WriteString(notice + "\n")
		want = append(want, "notice")
		input.WriteString(chunk("adapted") + "\n")
		want = append(want, "tool_call", "agent_message_chunk")
	}
	if _, err := agentOutputWriter.Write([]byte(input.String())); err != nil {
		t.Fatal(err)
	}
	_ = agentOutputWriter.Close()
	<-done
	<-conn.Done()
	if err := client.DrainSessionUpdates(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range log.Snapshot() {
		got = append(got, entry.Kind)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("logged order differs from the wire (%d entries, want %d):\n%v", len(got), len(want), got)
	}
}
