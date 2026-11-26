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
	"os/exec"
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
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
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
	loadError         error
}

func (agent *authReplayAgent) LoadSession(context.Context, sdkacp.LoadSessionRequest) (sdkacp.LoadSessionResponse, error) {
	agent.calls = append(agent.calls, "load_session")
	return sdkacp.LoadSessionResponse{}, agent.loadError
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
		Session:   session.SessionInfo{Directory: dir, Harness: "agy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Yolo {
		t.Fatal("ordinary ACP session must not enable yolo permissions")
	}
}

// A chat in a subfolder that recorded no definition root stages from the manifest above it.
func TestWorkerLaunchContextFindsTheManifestAboveTheFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("name = \"p\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, err := workerLaunchContext(WorkerLaunch{Directory: sub, Session: session.SessionInfo{Directory: sub, Harness: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(root); ctx.ProjectRoot != want && ctx.ProjectRoot != root {
		t.Fatalf("definition root = %s, want %s", ctx.ProjectRoot, root)
	}
}

func TestStartACPRequiresHarness(t *testing.T) {
	dir := t.TempDir()
	_, _, err := startACPConversation(context.Background(), WorkerLaunch{
		Transport: "acp",
		Directory: dir,
		Session:   session.SessionInfo{Directory: dir},
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
		Session:   session.SessionInfo{Directory: dir, Harness: "claude"},
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
	store := statetest.Open(t, t.TempDir())
	ctx := context.Background()
	info := session.SessionInfo{ID: "session-test", Transport: "acp", ConversationState: "idle"}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		t.Fatal(err)
	}
	log, err := NewConversationLog(ctx, store, info.ID, func(err error) { t.Error(err) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
	log.Append("permission_request", 1, json.RawMessage(`{"requestId":"p1"}`))
	conv.storeSettled()
	got, err := session.StoredSession(ctx, store.Reader, info.ID)
	if err != nil || got.ConversationState != "needs_you" {
		t.Fatalf("state without a reader=%q err=%v", got.ConversationState, err)
	}
}

// A state change is stored with the events behind it before anything acts on it; streamed
// chunks in between are left to the batching writer.
func TestConversationStateIsStoredOnTransitions(t *testing.T) {
	log := acp.NewEventLog(0)
	conv := NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil))
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
	conv.storeSettled()
	if last() != "working" {
		t.Fatalf("stored states after prompt = %v", states)
	}
	for i := range 1_000 {
		log.Append("agent_message_chunk", int64(i+2), json.RawMessage(`{"text":{"text":"x"}}`))
	}
	log.Append("turn_end", 2_000, json.RawMessage(`{}`))
	conv.storeSettled()
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
	data, err := json.Marshal(session.SessionInfo{AgentCapabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"agent_capabilities":`) ||
		strings.Contains(string(data), "private") || strings.Contains(string(data), "audio") || strings.Contains(string(data), `"list"`) {
		t.Fatalf("capability view contains unapproved fields: %s", data)
	}
	var got session.SessionInfo
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
	var old session.SessionInfo
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
	_, err := initializeACPSession(t.Context(), conn, t.TempDir(), "", "", "", chatTools{}, false, func(context.Context, sdkacp.InitializeResponse) error {
		if strings.Join(agent.calls, ",") != "initialize" {
			t.Fatalf("ACP calls before verified staging = %v", agent.calls)
		}
		return stageErr
	})
	if !errors.Is(err, stageErr) || strings.Join(agent.calls, ",") != "initialize" {
		t.Fatalf("failed staging allowed a session request: %v, %v", agent.calls, err)
	}
}

func TestReloadReplacesOnlyAnUnsavedNativeSession(t *testing.T) {
	missing := &sdkacp.RequestError{Code: -32002, Message: "Resource not found"}
	for _, tc := range []struct {
		name        string
		loadError   error
		replace     bool
		wantSession string
		wantCalls   string
	}{
		{"pristine chat", missing, true, "test-session", "initialize,load_session,new_session"},
		{"existing history", missing, false, "", "initialize,load_session"},
		{"other load error", sdkacp.NewAuthRequired(nil), true, "", "initialize,load_session"},
		{"existing native session", nil, true, "native", "initialize,load_session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil), loadError: tc.loadError}
			conn, cleanup := authReplayConnection(agent)
			defer cleanup()
			setup, err := initializeACPSession(t.Context(), conn, t.TempDir(), "native", "", "", chatTools{}, tc.replace, nil)
			if setup.AgentSessionID != tc.wantSession || (err != nil) != (tc.wantSession == "") {
				t.Fatalf("session = %q, error = %v", setup.AgentSessionID, err)
			}
			if calls := strings.Join(agent.calls, ","); calls != tc.wantCalls {
				t.Fatalf("ACP calls = %s, want %s", calls, tc.wantCalls)
			}
		})
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
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com", chatTools{}, false, nil)
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
	setup, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "", chatTools{}, false, nil)
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
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "source-session", "", chatTools{}, false, nil)
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
	_, err := initializeACPSession(context.Background(), clientConn, t.TempDir(), "", "", "grok.com", chatTools{}, false, nil)
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
		Session:   session.SessionInfo{ID: "test-session", Directory: directory, Harness: "claude", Mode: "default"},
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
	lines, derived := make(chan string, 8), make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		event := ""
		for scanner.Scan() {
			switch text := scanner.Text(); {
			case strings.HasPrefix(text, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(text, "event:"))
			case strings.HasPrefix(text, "data:") && event == "derived":
				derived <- text
			case strings.HasPrefix(text, "data:"):
				lines <- text
			case text == "":
				event = ""
			}
		}
		close(lines)
	}()
	// The server's fold comes first, so a browser shows the state before any entry arrives.
	if got := <-derived; !strings.Contains(got, `"working":true`) || !strings.Contains(got, `"through":1`) {
		t.Fatalf("first derived frame = %s", got)
	}
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
	log.Append("turn_end", 3, json.RawMessage(`{}`))
	for got := ""; !strings.Contains(got, `"through":3`); {
		select {
		case got = <-derived:
			if strings.Contains(got, `"through":3`) && !strings.Contains(got, `"conversation_state":"idle"`) {
				t.Fatalf("derived after turn end = %s", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("state change never reached the stream")
		}
	}
}

func TestACPAuthRequiredNamesAgentAndMachine(t *testing.T) {
	cause := fmt.Errorf("acp session/new: %w", sdkacp.NewAuthRequired(nil))
	err := explainACPAuthRequired(cause, base.Agy)
	if !strings.HasPrefix(err.Error(), "Antigravity"+NotSignedIn) || strings.Contains(err.Error(), "acp") {
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
	if len(entries) != 1 || !entries[0].SteeredPrompt() || !strings.Contains(string(entries[0].Payload), `"queueId":"q1"`) {
		t.Fatalf("only the injected steer is a prompt entry: %s", logEntryKinds(prompter.log))
	}
}

func TestSteerReachesAdapterOwnedTurn(t *testing.T) {
	prompter, agent := newHeldPrompter(t)
	defer close(agent.release)
	conv := NewConversationSession(prompter.log, prompter.broker())
	conv.steer = prompter.steer
	thread := func(status string) {
		prompter.log.Append("session_info_update", 1, MustRawJSON(map[string]any{
			"_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]string{"type": status}}},
		}))
	}
	thread("active")
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "look here", QueueID: "steer-autonomous"}, true); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	got := strings.Join(agent.methods, ",")
	agent.mu.Unlock()
	if got != "_session/steering" || len(queuedIDs(conv)) != 0 {
		t.Fatalf("methods = %q, queue = %v", got, queuedIDs(conv))
	}
	thread("idle")
	if got := conv.ConversationState(); got != "idle" {
		t.Fatalf("adapter-owned turn could not reach idle after steering: state = %q", got)
	}
}

func TestSteerWhenAdapterIsIdleFallsBackOnce(t *testing.T) {
	prompter, agent := newHeldPrompter(t)
	defer close(agent.release)
	agent.steering = "promptRequired"
	conv := NewConversationSession(prompter.log, prompter.broker())
	conv.steer = prompter.steer
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "look here", QueueID: "steer-idle"}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.received:
	case <-time.After(2 * time.Second):
		t.Fatal("idle adapter did not receive a fallback prompt")
	}
	agent.release <- struct{}{}
	waitConversationState(t, conv, "idle")
	agent.mu.Lock()
	got := strings.Join(agent.methods, ",")
	agent.mu.Unlock()
	if got != "_session/steering,session/prompt" {
		t.Fatalf("methods = %q", got)
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

// fakeAntigravityAgent speaks ACP over real SDK pipes like Antigravity CLI >= 1.5.0: it echoes
// prompts as user_message_chunk notifications and accepts a concurrent session/prompt while a
// command in the first prompt is still blocked.
type fakeAntigravityAgent struct {
	*acptest.ReplayAgent
	conn      *sdkacp.AgentSideConnection
	agentName string
	version   string
	meta      map[string]any

	mu            sync.Mutex
	methods       []string
	promptCount   int
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	steerArrived  chan string
	releaseSteer  chan struct{}
	thirdArrived  chan string
	releaseThird  chan struct{}
	cancelled     chan struct{}
	cancelOnce    sync.Once
	echoSteerLate bool
	// steerAnswersImmediately models an agent that acks the steered session/prompt before the
	// first prompt's turn ends, vs one that answers the first prompt first and the steer last.
	steerAnswersImmediately bool
	// wireTurnEndRace releases the first prompt immediately while the second runs a new turn.
	wireTurnEndRace bool
}

func (a *fakeAntigravityAgent) SetConnection(conn *sdkacp.AgentSideConnection) {
	a.conn = conn
	a.ReplayAgent.SetConnection(conn)
}

func (a *fakeAntigravityAgent) record(method string) {
	a.mu.Lock()
	a.methods = append(a.methods, method)
	a.mu.Unlock()
}

func (a *fakeAntigravityAgent) recordedMethods() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.methods)
}

func (a *fakeAntigravityAgent) Initialize(context.Context, sdkacp.InitializeRequest) (sdkacp.InitializeResponse, error) {
	a.record("initialize")
	return sdkacp.InitializeResponse{
		ProtocolVersion: sdkacp.ProtocolVersionNumber,
		AgentInfo:       &sdkacp.Implementation{Name: a.agentName, Version: a.version},
		AgentCapabilities: sdkacp.AgentCapabilities{
			LoadSession: true,
			Meta:        a.meta,
		},
	}, nil
}

func (a *fakeAntigravityAgent) NewSession(context.Context, sdkacp.NewSessionRequest) (sdkacp.NewSessionResponse, error) {
	a.record("session/new")
	return sdkacp.NewSessionResponse{SessionId: "agy-sess-1"}, nil
}

func (a *fakeAntigravityAgent) Cancel(context.Context, sdkacp.CancelNotification) error {
	a.record("session/cancel")
	a.cancelOnce.Do(func() { close(a.cancelled) })
	return nil
}

func (a *fakeAntigravityAgent) HandleExtensionMethod(_ context.Context, method string, _ json.RawMessage) (any, error) {
	a.record(method)
	return nil, sdkacp.NewMethodNotFound(method)
}

func (a *fakeAntigravityAgent) sendUpdate(ctx context.Context, sid sdkacp.SessionId, update sdkacp.SessionUpdate) {
	_ = a.conn.SessionUpdate(ctx, sdkacp.SessionNotification{SessionId: sid, Update: update})
}

func (a *fakeAntigravityAgent) Prompt(ctx context.Context, req sdkacp.PromptRequest) (sdkacp.PromptResponse, error) {
	a.record("session/prompt")
	text := ""
	for _, block := range req.Prompt {
		if block.Text != nil {
			text += block.Text.Text
		}
	}
	a.mu.Lock()
	a.promptCount++
	n := a.promptCount
	a.mu.Unlock()

	switch n {
	case 1:
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateUserMessageText(text))
		a.sendUpdate(ctx, req.SessionId, sdkacp.StartToolCall("cmd-1", "Run command 1"))
		close(a.firstEntered)
		select {
		case <-a.releaseFirst:
		case <-a.cancelled:
			return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonCancelled}, nil
		}
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateToolCall("cmd-1", sdkacp.WithUpdateStatus(sdkacp.ToolCallStatusCompleted)))
		if !a.wireTurnEndRace {
			a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateAgentMessageText("steered done"))
		}
		return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
	case 2:
		if a.steerArrived != nil {
			a.steerArrived <- text
		}
		if a.steerAnswersImmediately {
			if a.echoSteerLate {
				// Emit delayed echo after tool updates are already in the stream.
				<-a.releaseSteer
				a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateUserMessageText(text))
			}
			return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
		}
		if a.releaseSteer != nil {
			<-a.releaseSteer
		}
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateUserMessageText(text))
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateAgentMessageText("after steer"))
		return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
	default:
		if a.thirdArrived != nil {
			a.thirdArrived <- text
		}
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateUserMessageText(text))
		a.sendUpdate(ctx, req.SessionId, sdkacp.UpdateAgentMessageText("third reply"))
		if a.releaseThird != nil {
			<-a.releaseThird
		}
		return sdkacp.PromptResponse{StopReason: sdkacp.StopReasonEndTurn}, nil
	}
}

func newFakeAntigravitySession(t *testing.T, agent *fakeAntigravityAgent) (*ConversationSession, *acpPrompter) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "agy")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_PATH", stub)
	agyHarness, err := registry.ByTarget(base.Agy)
	if err != nil {
		t.Fatal(err)
	}
	acpLaunch, err := agyHarness.LaunchACP(base.LaunchContext{
		ProjectRoot:   dir,
		WorkspaceRoot: dir,
		Mode:          mode.ImplicitEffective(),
	})
	if err != nil {
		t.Fatal(err)
	}
	log := acp.NewEventLog(0)
	prompter := &acpPrompter{log: log, harness: "agy"}
	broker := prompter.broker()
	conv := NewConversationSession(log, broker)
	client := acp.NewHarnessClient(dir, log, broker)
	client.SuppressUserEcho = prompter.suppressUserEcho
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	clientConn := client.NewClientSideConnection(c2aW, a2cR)
	agentConn := sdkacp.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	prompter.mu.Lock()
	prompter.conn = clientConn
	prompter.mu.Unlock()
	t.Cleanup(func() {
		for _, pipe := range []io.Closer{c2aW, a2cW, c2aR, a2cR} {
			_ = pipe.Close()
		}
	})
	setup, err := initializeACPSession(t.Context(), clientConn, dir, "", "", "", chatTools{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	prompter.ready(setup)
	conv.agentVersion = setup.InitInfo.AgentVersion
	conv.agentSessionID = setup.AgentSessionID
	conv.SetAgentCapabilities(setup.AgentCapabilities)
	configureACPSteering(conv, prompter, acpLaunch, setup)
	return conv, prompter
}

func TestAntigravityPromptSteerReachesBlockedTurnWithoutCancel(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		steerAnswersImmediately bool
	}{
		{name: "steer_answers_before_first_prompt", steerAnswersImmediately: true},
		{name: "first_prompt_answers_before_steer", steerAnswersImmediately: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &fakeAntigravityAgent{
				ReplayAgent:             acptest.NewReplayAgent(nil),
				agentName:               "Antigravity ACP Server",
				version:                 "1.5.17",
				meta:                    map[string]any{"antigravity": map[string]any{"tier": true}},
				firstEntered:            make(chan struct{}),
				releaseFirst:            make(chan struct{}),
				steerArrived:            make(chan string, 1),
				releaseSteer:            make(chan struct{}),
				cancelled:               make(chan struct{}, 1),
				echoSteerLate:           true,
				steerAnswersImmediately: tc.steerAnswersImmediately,
			}
			conv, prompter := newFakeAntigravitySession(t, agent)
			if caps := conv.AgentCapabilitiesView(); caps == nil || !caps.Steering {
				t.Fatalf("Antigravity 1.5.17 capabilities = %+v, want Steering=true", caps)
			}

			if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "run 3 commands"}, false); err != nil {
				t.Fatal(err)
			}
			select {
			case <-agent.firstEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("first prompt never reached blocked command")
			}

			// Steer while the first command is still blocked.
			if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "s1", Text: "stop after command 1"}, true); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-agent.steerArrived:
				if got != "stop after command 1" {
					t.Fatalf("steered prompt text = %q", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("second session/prompt did not arrive before blocked first command released")
			}

			// Release in the order under test, ensuring delayed echo arrives after tool_call_update.
			if tc.steerAnswersImmediately {
				close(agent.releaseSteer)
				// Wait briefly to make sure the steer response alone does not end the turn.
				time.Sleep(30 * time.Millisecond)
				if state := conv.ConversationState(); state != "working" {
					t.Fatalf("state after early steer response = %q, want working", state)
				}
				close(agent.releaseFirst)
			} else {
				close(agent.releaseFirst)
				time.Sleep(30 * time.Millisecond)
				if state := conv.ConversationState(); state != "working" {
					t.Fatalf("state after first response while steer still open = %q, want working", state)
				}
				close(agent.releaseSteer)
			}

			waitConversationState(t, conv, "idle")

			methods := agent.recordedMethods()
			if slices.Contains(methods, "session/cancel") || slices.Contains(methods, "_session/steering") {
				t.Fatalf("unexpected wire methods %v; live steering must not cancel or call _session/steering", methods)
			}
			if got := strings.Join(methods, ","); got != "initialize,session/new,session/prompt,session/prompt" {
				t.Fatalf("wire methods = %q", got)
			}

			entries := prompter.log.Snapshot()
			turnEnds := 0
			for _, entry := range entries {
				if entry.Kind == "turn_end" {
					turnEnds++
				}
			}
			if turnEnds != 1 {
				t.Fatalf("turn_end count = %d, want 1 (%s)", turnEnds, logEntryKinds(prompter.log))
			}

			reduced := acp.ReduceLog(entries)
			var userTexts []string
			for _, item := range reduced.Items {
				if item.Kind == "user_message_chunk" {
					var body struct {
						Text struct {
							Text string `json:"text"`
						} `json:"text"`
					}
					_ = json.Unmarshal(item.Data, &body)
					userTexts = append(userTexts, body.Text.Text)
				}
			}
			if !slices.Equal(userTexts, []string{"run 3 commands", "stop after command 1"}) {
				t.Fatalf("visible user prompts = %v, want exactly one per prompt (entries: %s)", userTexts, logEntryKinds(prompter.log))
			}
		})
	}
}

func TestAntigravityPromptSteerIdleAndTurnEndRaces(t *testing.T) {
	t.Run("already_idle_falls_back_to_normal_prompt", func(t *testing.T) {
		agent := &fakeAntigravityAgent{
			ReplayAgent:  acptest.NewReplayAgent(nil),
			agentName:    "Antigravity ACP Server",
			version:      "1.5.17",
			meta:         map[string]any{"antigravity": map[string]any{"tier": true}},
			firstEntered: make(chan struct{}),
			releaseFirst: make(chan struct{}),
			cancelled:    make(chan struct{}, 1),
		}
		conv, prompter := newFakeAntigravitySession(t, agent)
		close(agent.releaseFirst)

		if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "idle-steer", Text: "start fresh"}, true); err != nil {
			t.Fatal(err)
		}
		waitConversationState(t, conv, "idle")

		if got := strings.Join(agent.recordedMethods(), ","); got != "initialize,session/new,session/prompt" {
			t.Fatalf("wire methods = %q", got)
		}
		reduced := acp.ReduceLog(prompter.log.Snapshot())
		userCount := 0
		for _, item := range reduced.Items {
			if item.Kind == "user_message_chunk" {
				userCount++
			}
		}
		if userCount != 1 {
			t.Fatalf("visible user prompts = %d, want 1", userCount)
		}
	})

	t.Run("wire_turn_end_race_keeps_turn_open_until_second_prompt_finishes", func(t *testing.T) {
		agent := &fakeAntigravityAgent{
			ReplayAgent:     acptest.NewReplayAgent(nil),
			agentName:       "Antigravity ACP Server",
			version:         "1.5.17",
			meta:            map[string]any{"antigravity": map[string]any{"tier": true}},
			firstEntered:    make(chan struct{}),
			releaseFirst:    make(chan struct{}),
			steerArrived:    make(chan string, 1),
			releaseSteer:    make(chan struct{}),
			thirdArrived:    make(chan string, 1),
			releaseThird:    make(chan struct{}),
			cancelled:       make(chan struct{}, 1),
			wireTurnEndRace: true,
		}
		conv, prompter := newFakeAntigravitySession(t, agent)

		if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "first"}, false); err != nil {
			t.Fatal(err)
		}
		<-agent.firstEntered

		// Steer just before the first prompt finishes on the server.
		if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "race-steer", Text: "second"}, true); err != nil {
			t.Fatal(err)
		}
		if got := <-agent.steerArrived; got != "second" {
			t.Fatalf("steered prompt = %q", got)
		}
		// Queue a third ordinary prompt while the steered prompt is still open.
		if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "queued-3", Text: "third"}, false); err != nil {
			t.Fatal(err)
		}

		// First prompt ends immediately; the steered prompt is now running as its own server turn.
		close(agent.releaseFirst)
		select {
		case got := <-agent.thirdArrived:
			t.Fatalf("queued third prompt %q ran before the steered prompt finished", got)
		case <-time.After(50 * time.Millisecond):
		}
		if state := conv.ConversationState(); state != "working" {
			t.Fatalf("state after first prompt ended during wire race = %q, want working", state)
		}

		// Release the steered prompt; only now should the logical turn end and third prompt run.
		close(agent.releaseSteer)
		select {
		case got := <-agent.thirdArrived:
			if got != "third" {
				t.Fatalf("third prompt = %q", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("queued third prompt never ran after steered prompt finished")
		}
		close(agent.releaseThird)
		waitConversationState(t, conv, "idle")

		if slices.Contains(agent.recordedMethods(), "session/cancel") {
			t.Fatalf("unexpected session/cancel in wire race: %v", agent.recordedMethods())
		}
		reduced := acp.ReduceLog(prompter.log.Snapshot())
		var userTexts []string
		for _, item := range reduced.Items {
			if item.Kind == "user_message_chunk" {
				var body struct {
					Text struct {
						Text string `json:"text"`
					} `json:"text"`
				}
				_ = json.Unmarshal(item.Data, &body)
				userTexts = append(userTexts, body.Text.Text)
			}
		}
		if !slices.Equal(userTexts, []string{"first", "second", "third"}) {
			t.Fatalf("visible user prompts = %v, want [first second third]", userTexts)
		}
	})
}

func TestAntigravityPromptSteerVersionFence(t *testing.T) {
	agent := &fakeAntigravityAgent{
		ReplayAgent:  acptest.NewReplayAgent(nil),
		agentName:    "Antigravity ACP Server",
		version:      "1.4.12",
		meta:         map[string]any{"antigravity": map[string]any{"tier": true}},
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		steerArrived: make(chan string, 1),
		releaseSteer: make(chan struct{}),
		cancelled:    make(chan struct{}, 1),
	}
	conv, _ := newFakeAntigravitySession(t, agent)
	if caps := conv.AgentCapabilitiesView(); caps != nil && caps.Steering {
		t.Fatalf("Antigravity 1.4.12 should not advertise Steering: %+v", caps)
	}
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{Text: "first"}, false); err != nil {
		t.Fatal(err)
	}
	<-agent.firstEntered
	if _, _, err := conv.Submit(t.Context(), acp.PromptInput{QueueID: "old-steer", Text: "redirect"}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("pre-1.5.0 Antigravity did not fall back to session/cancel")
	}
	close(agent.releaseSteer)
	waitConversationState(t, conv, "idle")
	if got := strings.Join(agent.recordedMethods(), ","); got != "initialize,session/new,session/prompt,session/cancel,session/prompt" {
		t.Fatalf("wire methods for pre-1.5.0 = %q", got)
	}
}

// A chat's agent process gets the shared secrets; a probe that is no session's agent gets none, and
// a gateway that cannot answer never stops the start.
func TestVaultSecretsInACPAgentEnvironment(t *testing.T) {
	home := t.TempDir()
	status := http.StatusOK
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/session-vault/agent-env" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 64) {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(status)
		WriteJSON(w, AgentSecrets{Env: map[string]string{"VAULT_TOKEN": "s3cret-value"}, Credentials: []string{"mcpc-1"}})
	}))
	t.Cleanup(gateway.Close)
	record, _ := json.Marshal(NodeRecord{Address: gateway.Listener.Addr().String(), Secret: strings.Repeat("x", 64)})
	if err := os.MkdirAll(filepath.Join(home, "network"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "network", "node.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := func(secretsHome string) []string {
		t.Helper()
		launch := WorkerLaunch{Session: session.SessionInfo{ID: "session-vault"}, Environment: []string{"VAULT_TOKEN=from-the-machine"}, secretsHome: secretsHome}
		acpLaunch := base.ACPLaunch{Cmd: exec.Command("agent")}
		client := acp.NewHarnessClient(home, acp.NewEventLog(0), acp.NewBroker(nil, nil, nil, nil))
		process, err := connectACP(t.Context(), launch, home, &acpLaunch, client, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		_ = process.agentOutW.Close()
		_ = process.clientInW.Close()
		_ = process.stdin.Close()
		<-process.adapted
		if !slices.Equal(launch.Environment, []string{"VAULT_TOKEN=from-the-machine"}) {
			t.Fatal("the launch itself changed")
		}
		return process.cmd.Env
	}
	// The last entry of a name is the one the process sees.
	if got := environment(home); !slices.Contains(got, "VAULT_TOKEN=s3cret-value") || slices.Index(got, "VAULT_TOKEN=s3cret-value") < slices.Index(got, "VAULT_TOKEN=from-the-machine") {
		t.Fatalf("agent environment %v", got)
	}
	if got := environment(""); slices.Contains(got, "VAULT_TOKEN=s3cret-value") {
		t.Fatal("a probe got a shared secret")
	}
	status = http.StatusServiceUnavailable
	if got := environment(home); slices.Contains(got, "VAULT_TOKEN=s3cret-value") {
		t.Fatal("a refused answer was used")
	}
}
