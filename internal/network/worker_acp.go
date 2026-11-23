package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/obs"
	packSync "github.com/olegpustovit/agentpack/internal/sync"

	"github.com/olegpustovit/agentpack/internal/paths"
)

func startACPConversation(ctx context.Context, launch WorkerLaunch, sessionDir string, conv *ConversationSession) (conversation *ConversationSession, cleanup func(), result error) {
	log := conv.log
	defer func() {
		if result != nil {
			closeACPEventLog(log)
		}
		if conversation != nil {
			conversation.sessionID = filepath.Base(sessionDir)
		}
	}()
	if err := paths.EnsurePrivateDirectory(sessionDir); err != nil {
		return nil, nil, fmt.Errorf("protect ACP session directory: %w", err)
	}
	switch os.Getenv("AGENTPACK_TEST_ACP") {
	case "1":
		if launch.ForkSessionID != "" {
			return nil, nil, errors.New("forking is unavailable in ACP fixture mode")
		}
		fixture := os.Getenv("AGENTPACK_TEST_ACP_FIXTURE")
		if fixture == "" {
			return nil, nil, errors.New("AGENTPACK_TEST_ACP_FIXTURE is required")
		}
		entries, err := loadEventLogFixtureEntries(fixture)
		if err != nil {
			return nil, nil, err
		}
		if len(log.Snapshot()) == 0 {
			for _, entry := range entries {
				log.Append(entry.Kind, entry.TS, entry.Payload)
			}
		}
		conv.broker = acp.NewBroker(nil, nil, nil, nil)
		return conv, func() { closeACPEventLog(log) }, nil
	case "2":
		fixture := os.Getenv("AGENTPACK_TEST_ACP_FIXTURE")
		if fixture == "" {
			return nil, nil, errors.New("AGENTPACK_TEST_ACP_FIXTURE is required")
		}
		entries, err := loadEventLogFixtureEntries(fixture)
		if err != nil {
			return nil, nil, err
		}
		return startACPReplayAgent(ctx, launch, sessionDir, entries, conv)
	}

	target, err := harnessTarget(launch.Session.Harness)
	if err != nil {
		return nil, nil, err
	}
	harness, err := registry.ByTarget(target)
	if err != nil {
		return nil, nil, err
	}
	done := startStep(ctx, "acp_launch_context")
	launchCtx, err := workerLaunchContext(launch)
	done(err)
	if err != nil {
		return nil, nil, err
	}
	var stageDone <-chan error
	if launch.StageInWorker {
		if !harness.ACPInitializeBeforeStage() {
			return nil, nil, errors.New("ACP adapter requires staging before startup")
		}
		stageCtx, cancelStage := context.WithCancel(WithPeerCache(ctx, ""))
		staged := make(chan error, 1)
		stageDone = staged
		go func() {
			defer close(staged)
			service := packSync.NewService()
			_, _, err := service.SyncForLaunchOptions(stageCtx, launchCtx.ProjectRoot, packSync.SyncOptions{
				Mode: launchCtx.Mode.Name(), Target: &target, WorkspaceRoot: launchCtx.WorkspaceRoot,
			})
			staged <- err
		}()
		defer func() {
			cancelStage()
			<-staged
		}()
	}
	done = startStep(ctx, "acp_adapter")
	acpLaunch, err := harness.ACP(launchCtx)
	done(err)
	if err != nil {
		return nil, nil, err
	}
	return startACPAgentProcess(ctx, launch, launchCtx, harness, acpLaunch, sessionDir, conv, stageDone)
}

func startACPReplayAgent(ctx context.Context, launch WorkerLaunch, sessionDir string, entries []acp.LogEntry, conv *ConversationSession) (*ConversationSession, func(), error) {
	log := conv.log
	workspace := launch.Session.Directory
	if workspace == "" {
		workspace = launch.Directory
	}

	prompter := &acpPrompter{log: log, sessionDir: sessionDir, harness: launch.Session.Harness}
	broker := prompter.broker()
	conv.mu.Lock()
	conv.broker = broker
	conv.workspace = workspace
	conv.agentExited = false
	replaying := launch.Session.AgentSessionID != "" && conv.lastSeq > 0
	conv.mu.Unlock()
	log.SetReplaying(replaying)
	defer log.SetReplaying(false)
	client := acp.NewHarnessClient(workspace, log, broker)
	client.AutoApprove = conv.BypassingPermissions
	client.StoreImage = conv.storeImage
	agent := acptest.NewReplayAgent(entries)
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	clientConn := client.NewClientSideConnection(c2aW, a2cR)
	agentConn := sdkacp.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	prompter.mu.Lock()
	prompter.conn = clientConn
	prompter.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	cleanup := func() {
		cancel()
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		_ = c2aW.Close()
		_ = a2cW.Close()
		_ = c2aR.Close()
		_ = a2cR.Close()
		finishACPConversation(ctx, conv, client, clientConn)
	}
	setup, err := initializeACPSession(ctx, clientConn, workspace, launch.Session.AgentSessionID, launch.ForkSessionID, "", launch.tools, nil)
	log.SetReplaying(false)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	prompter.ready(setup)
	launch.Session.AgentSessionID = setup.AgentSessionID
	conv.agentVersion = setup.AgentVersion
	conv.agentSessionID = setup.AgentSessionID
	conv.SetConfigOptions(setup.ConfigOptions)
	conv.SetModes(setup.Modes)
	conv.SetAgentCapabilities(setup.AgentCapabilities)
	return conv, cleanup, nil
}

func startACPAgentProcess(ctx context.Context, launch WorkerLaunch, launchCtx base.LaunchContext, harness base.Harness, acpLaunch base.ACPLaunch, sessionDir string, conv *ConversationSession, stageDone <-chan error) (*ConversationSession, func(), error) {
	log := conv.log
	cmd := acpLaunch.Cmd
	if cmd == nil {
		return nil, nil, errors.New("harness ACP launch returned nil command")
	}
	workspace := launchCtx.WorkspaceRoot
	agentOutR, agentOutW := io.Pipe()
	clientInR, clientInW := io.Pipe()
	cmd.Stdout = agentOutW
	stderr := &diagnosticStderr{}
	defer func() {
		if event := obs.From(ctx); event != nil {
			event.Set("stderr", stderr.Redacted())
		}
	}()
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	cmd.Dir = workspace
	cmd.Env = workerAgentEnvironment(launch.Environment, cmd.Env, workspace)
	cmd.Env = append(cmd.Env, "AGENTPACK_TRACE="+obs.ParseTraceParent(obs.TraceParent(ctx)).ID)

	stdinWriter, err := cmd.StdinPipe()
	if err != nil {
		closeACPEventLog(log)
		return nil, nil, fmt.Errorf("open ACP adapter stdin: %w", err)
	}
	prompter := &acpPrompter{log: log, sessionDir: sessionDir, harness: launch.Session.Harness}
	// A fresh agent on a chat with history took it over from another agent: it gets that history once.
	if launch.Session.AgentSessionID == "" && launch.ForkSessionID == "" {
		reader := ""
		if launch.Session.Tools && launch.tools.token != "" {
			reader = launch.Session.ID
		}
		prompter.handoff = handoffContext(log.Snapshot(), reader)
	}
	broker := prompter.broker()
	conv.mu.Lock()
	conv.broker = broker
	conv.workspace = workspace
	conv.agentExited = false
	replaying := launch.Session.AgentSessionID != "" && conv.lastSeq > 0
	conv.mu.Unlock()
	log.SetReplaying(replaying)
	defer log.SetReplaying(false)
	client := acp.NewHarnessClient(workspace, log, broker)
	client.AutoApprove = conv.BypassingPermissions
	client.StoreImage = conv.storeImage
	rewriter := &rewriteStdin{w: stdinWriter, rewrite: func(line []byte) []byte {
		line = acp.AddSessionCapabilities(line)
		if acpLaunch.Rewrite != nil {
			line = acpLaunch.Rewrite(line)
		}
		return line
	}}
	stdinToAgent := io.Writer(rewriter)
	clientConn := client.NewClientSideConnection(stdinToAgent, clientInR)
	prompter.mu.Lock()
	prompter.conn = clientConn
	prompter.mu.Unlock()
	adaptDone := make(chan struct{})
	go func() {
		defer close(adaptDone)
		adaptAgentStdout(agentOutR, clientInW, acpLaunch.AdaptAgent, log)
	}()

	launchCtx.Command = cmd
	if err := cmd.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = agentOutR.Close()
		_ = agentOutW.Close()
		_ = clientInR.Close()
		_ = clientInW.Close()
		<-adaptDone
		closeACPEventLog(log)
		return nil, nil, err
	}
	if err := harness.AfterLaunch(launchCtx); err != nil {
		_ = stdinWriter.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = agentOutW.Close()
		<-adaptDone
		appendACPAgentExit(log)
		closeACPEventLog(log)
		_ = clientInR.Close()
		_ = clientInW.Close()
		return nil, nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	agentDone := make(chan struct{})
	var stopping atomic.Bool
	exitCode := &atomic.Int32{}
	exitCode.Store(-1)
	cleanup := func() {
		stopping.Store(true)
		cancel()
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		// Release an SDK cancellation write before waiting for the rewriter mutex.
		_ = stdinWriter.Close()
		if rewriter != nil {
			_ = rewriter.Close()
		}
		_ = cmd.Process.Kill()
		<-agentDone
		_ = agentOutR.Close()
		_ = clientInR.Close()
		finishACPConversation(ctx, conv, client, clientConn)
	}
	obs.Go("worker.acp_agent", func() {
		err := cmd.Wait()
		if cmd.ProcessState != nil {
			exitCode.Store(int32(cmd.ProcessState.ExitCode()))
		}
		_ = agentOutW.Close()
		<-adaptDone
		if !stopping.Load() {
			name := "exit"
			if err != nil {
				name = "unexpected_exit"
			}
			_, event := obs.Start(ctx, "worker", name)
			event.Set("session_id", filepath.Base(sessionDir))
			event.Set("harness", launch.Session.Harness)
			event.Status = int(exitCode.Load())
			event.Set("stderr", stderr.Redacted())
			event.Fail(err)
			event.End()
		}
		appendACPAgentExit(log)
		cancel()
		close(agentDone)
	})

	beforeSession := func(initCtx context.Context) error {
		if stageDone != nil {
			select {
			case err := <-stageDone:
				if err != nil {
					return fmt.Errorf("stage ACP session: %w", err)
				}
			case <-initCtx.Done():
				return initCtx.Err()
			}
		}
		if acpLaunch.PrepareSession != nil {
			rewrite, err := acpLaunch.PrepareSession()
			if err != nil {
				return err
			}
			rewriter.mu.Lock()
			acpLaunch.Rewrite = rewrite
			rewriter.mu.Unlock()
		}
		return nil
	}
	done := startStep(ctx, "acp_handshake")
	setup, err := initializeACPSession(ctx, clientConn, workspace, launch.Session.AgentSessionID, launch.ForkSessionID, acpLaunch.AuthenticateMethodID, launch.tools, beforeSession)
	done(err)
	log.SetReplaying(false)
	if err != nil {
		cleanup()
		return nil, nil, explainACPAuthRequired(err, harness.ID())
	}
	prompter.ready(setup)
	launch.Session.AgentSessionID = setup.AgentSessionID
	conv.stderr = stderr
	conv.agentExitCode = exitCode
	conv.agentVersion = setup.AgentVersion
	conv.agentSessionID = setup.AgentSessionID
	conv.SetConfigOptions(setup.ConfigOptions)
	conv.SetModes(setup.Modes)
	conv.SetAgentCapabilities(setup.AgentCapabilities)
	if acpLaunch.ClientPermissionBypass {
		conv.mu.Lock()
		conv.clientMode = "default"
		conv.mu.Unlock()
	}
	conv.mu.Lock()
	conv.queuesPrompts = acpLaunch.QueuesPrompts
	if acpLaunch.Steers {
		conv.steer = prompter.steer
		if conv.agentCapabilities == nil {
			conv.agentCapabilities = &AgentCapabilities{}
		}
		conv.agentCapabilities.Steering = true
	}
	conv.mu.Unlock()
	if acpLaunch.UnpromptedUsageSettle > 0 {
		conv.SetUnpromptedUsageSettle(acpLaunch.UnpromptedUsageSettle)
	}
	return conv, cleanup, nil
}

// startStep records how long one session start step took, so a slow start names its cause.
func startStep(ctx context.Context, name string) func(error) {
	_, event := obs.Start(ctx, "worker", name)
	return func(err error) {
		event.Fail(err)
		event.End()
	}
}

type acpSessionSetup struct {
	AgentVersion      string
	AgentSessionID    string
	ConfigOptions     json.RawMessage
	Modes             json.RawMessage
	AgentCapabilities *AgentCapabilities
}

func agentCapabilitiesView(capabilities sdkacp.AgentCapabilities) *AgentCapabilities {
	view := &AgentCapabilities{LoadSession: capabilities.LoadSession}
	if capabilities.PromptCapabilities.Image {
		view.PromptCapabilities = &AgentPromptCapabilities{Image: true}
	}
	if capabilities.SessionCapabilities.Resume != nil || capabilities.SessionCapabilities.Fork != nil {
		session := &AgentSessionCapabilities{}
		if capabilities.SessionCapabilities.Resume != nil {
			session.Resume = &struct{}{}
		}
		if capabilities.SessionCapabilities.Fork != nil {
			session.Fork = &struct{}{}
		}
		view.SessionCapabilities = session
	}
	if !view.LoadSession && view.PromptCapabilities == nil && view.SessionCapabilities == nil {
		return nil
	}
	return view
}

func initializeACPClient(ctx context.Context, conn *sdkacp.ClientSideConnection, authenticateMethodID string) (sdkacp.InitializeResponse, error) {
	initialize, err := conn.Initialize(ctx, acp.InitializeRequest())
	if err != nil {
		return sdkacp.InitializeResponse{}, fmt.Errorf("acp initialize: %w", err)
	}
	if authenticateMethodID != "" {
		found := false
		for _, method := range initialize.AuthMethods {
			switch {
			case method.Agent != nil && method.Agent.Id == authenticateMethodID:
				found = true
			case method.EnvVar != nil && method.EnvVar.Id == authenticateMethodID:
				found = true
			case method.Terminal != nil && method.Terminal.Id == authenticateMethodID:
				found = true
			}
		}
		if !found {
			return sdkacp.InitializeResponse{}, fmt.Errorf("acp authentication method %q was not advertised", authenticateMethodID)
		}
		if _, err := conn.Authenticate(ctx, sdkacp.AuthenticateRequest{MethodId: authenticateMethodID}); err != nil {
			return sdkacp.InitializeResponse{}, fmt.Errorf("acp authenticate: %w", err)
		}
	}
	return initialize, nil
}

func initializeACPSession(ctx context.Context, conn *sdkacp.ClientSideConnection, workspace, loadID, forkSessionID, authenticateMethodID string, tools chatTools, beforeSession func(context.Context) error) (acpSessionSetup, error) {
	initCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if loadID != "" && forkSessionID != "" {
		return acpSessionSetup{}, errors.New("ACP session cannot be resumed and forked at the same time")
	}
	initialize, err := initializeACPClient(initCtx, conn, authenticateMethodID)
	if err != nil {
		return acpSessionSetup{}, err
	}
	if beforeSession != nil {
		if err := beforeSession(initCtx); err != nil {
			return acpSessionSetup{}, err
		}
	}
	setup := acpSessionSetup{AgentCapabilities: agentCapabilitiesView(initialize.AgentCapabilities)}
	if initialize.AgentInfo != nil {
		setup.AgentVersion = initialize.AgentInfo.Version
	}
	servers := tools.servers(initialize.AgentCapabilities.McpCapabilities)
	if forkSessionID != "" {
		if initialize.AgentCapabilities.SessionCapabilities.Fork == nil {
			return acpSessionSetup{}, errors.New("acp agent does not advertise session.fork")
		}
		resp, err := conn.UnstableForkSession(initCtx, sdkacp.UnstableForkSessionRequest{
			SessionId:  sdkacp.SessionId(forkSessionID),
			Cwd:        workspace,
			McpServers: tools.unstableServers(initialize.AgentCapabilities.McpCapabilities),
		})
		if err != nil {
			return acpSessionSetup{}, fmt.Errorf("acp session/fork: %w", err)
		}
		setup.AgentSessionID = string(resp.SessionId)
		if setup.AgentSessionID == "" || setup.AgentSessionID == forkSessionID {
			return acpSessionSetup{}, errors.New("acp session/fork returned an invalid new session ID")
		}
		setup.ConfigOptions = marshalUnstableConfigOptions(resp.ConfigOptions)
		setup.Modes = marshalSessionModes(resp.Modes)
		return setup, nil
	}
	if loadID != "" {
		resp, err := conn.LoadSession(initCtx, sdkacp.LoadSessionRequest{
			SessionId:  sdkacp.SessionId(loadID),
			Cwd:        workspace,
			McpServers: servers,
		})
		if err != nil {
			return acpSessionSetup{}, fmt.Errorf("acp session/load: %w", err)
		}
		setup.AgentSessionID = loadID
		setup.ConfigOptions = marshalConfigOptions(resp.ConfigOptions)
		setup.Modes = marshalSessionModes(resp.Modes)
		return setup, nil
	}
	resp, err := conn.NewSession(initCtx, sdkacp.NewSessionRequest{
		Cwd:        workspace,
		McpServers: servers,
	})
	if err != nil {
		return acpSessionSetup{}, fmt.Errorf("acp session/new: %w", err)
	}
	setup.AgentSessionID = string(resp.SessionId)
	setup.ConfigOptions = marshalConfigOptions(resp.ConfigOptions)
	setup.Modes = marshalSessionModes(resp.Modes)
	return setup, nil
}

// explainACPAuthRequired turns an agent's bare "Authentication required" into a sentence that names
// the agent and machine; the chat then offers to sign the agent in.
func explainACPAuthRequired(err error, target base.Target) error {
	var request *sdkacp.RequestError
	if !errors.As(err, &request) || request.Code != sdkacp.NewAuthRequired(nil).Code {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "this machine"
	}
	// The gateway reads this sentence from the worker's last line to offer a sign-in; see failStart.
	// No final period: the CLI's error renderer ends the sentence.
	return errors.New(base.DisplayName(target) + notSignedIn + host)
}

// notSignedIn joins an agent's name and a machine's in the start failure of an agent that needs a sign-in.
const notSignedIn = " is not signed in on "

func marshalConfigOptions(opts []sdkacp.SessionConfigOption) json.RawMessage {
	if opts == nil {
		return nil
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil
	}
	return raw
}

func marshalUnstableConfigOptions(opts []sdkacp.UnstableSessionConfigOption) json.RawMessage {
	if opts == nil {
		return nil
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil
	}
	return raw
}

func marshalSessionModes(modes *sdkacp.SessionModeState) json.RawMessage {
	if modes == nil {
		return nil
	}
	return mustRawJSON(modes)
}

func harnessTarget(name string) (base.Target, error) {
	if name == "" {
		return "", errors.New("ACP transport requires session harness")
	}
	if name == "agent" {
		name = "cursor"
	}
	return base.ParseTarget(name)
}

func workerLaunchContext(launch WorkerLaunch) (base.LaunchContext, error) {
	workspace := launch.Session.Directory
	if workspace == "" {
		workspace = launch.Directory
	}
	if !filepath.IsAbs(workspace) {
		return base.LaunchContext{}, errors.New("workspace must be absolute")
	}
	definition := launch.Session.DefinitionRoot
	if definition == "" {
		definition = workspace
	}
	modeName := launch.Session.Mode
	if modeName == "" {
		modeName = mode.DefaultName
	}
	effective, err := resolveWorkerMode(definition, modeName)
	if err != nil {
		return base.LaunchContext{}, err
	}
	return base.LaunchContext{
		ProjectRoot:   definition,
		WorkspaceRoot: workspace,
		Mode:          effective,
	}, nil
}

func resolveWorkerMode(definitionRoot, name string) (mode.Effective, error) {
	if name == mode.DefaultName {
		return mode.ImplicitEffective(), nil
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return mode.Effective{}, err
	}
	definition, ok := project.ModeDefinition(name)
	if !ok {
		return mode.Effective{}, fmt.Errorf("unknown mode: %s", name)
	}
	return mode.NewEffective(name, definition, nil)
}

func workerAgentEnvironment(launchEnv, cmdEnv []string, workspace string) []string {
	merged := append([]string(nil), launchEnv...)
	if merged == nil {
		merged = os.Environ()
	}
	if cmdEnv != nil {
		merged = append(merged, cmdEnv...)
	}
	filtered := merged[:0]
	for _, entry := range merged {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "AGENTPACK_SESSION_WORKER") || strings.EqualFold(key, "AGENTPACK_SESSION_ID") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(withPWD(filtered, workspace), "AGENTPACK_SESSION=1")
}

func withPWD(env []string, directory string) []string {
	out := append([]string(nil), env...)
	replaced := false
	for i, entry := range out {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PWD") {
			out[i] = "PWD=" + directory
			replaced = true
			break
		}
	}
	if !replaced {
		out = append(out, "PWD="+directory)
	}
	return out
}

type rewriteStdin struct {
	mu      sync.Mutex
	carry   []byte
	w       io.Writer
	rewrite func([]byte) []byte
}

func (r *rewriteStdin) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	data := append(r.carry, p...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			r.carry = data
			return n, nil
		}
		line := data[:i]
		data = data[i+1:]
		out := line
		if r.rewrite != nil {
			out = r.rewrite(line)
		}
		if _, err := r.w.Write(append(out, '\n')); err != nil {
			return n, err
		}
	}
}

func (r *rewriteStdin) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.carry) == 0 {
		return nil
	}
	out := r.carry
	if r.rewrite != nil {
		out = r.rewrite(r.carry)
	}
	_, err := r.w.Write(append(out, '\n'))
	r.carry = nil
	return err
}
func appendACPAgentExit(log *acp.EventLog) {
	log.Append("agent_exit", time.Now().UnixMilli(), json.RawMessage(`{}`))
}
func finishACPConversation(ctx context.Context, conversation *ConversationSession, client *acp.HarnessClient, connection *sdkacp.ClientSideConnection) {
	conversation.mu.Lock()
	conversation.agentExited = true
	conversation.mu.Unlock()
	<-connection.Done()
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := client.DrainSessionUpdates(drain); err != nil {
		conversation.log.MarkTruncated()
		_, event := obs.Start(ctx, "worker", "drain_conversation")
		event.Set("session_id", conversation.sessionID)
		event.Fail(err)
		event.End()
	}
	conversation.writes.Lock()
	defer conversation.writes.Unlock()
	closeACPEventLog(conversation.log)
}

func closeACPEventLog(log *acp.EventLog) {
	if err := log.Close(); err != nil && err != errSessionArchived {
		fmt.Fprintln(os.Stderr, "warning: ACP conversation log could not be persisted")
	}
}

func adaptAgentStdout(r io.ReadCloser, w io.WriteCloser, adapt func([]byte) base.AgentAdaptResult, log *acp.EventLog) {
	adaptAgentStdoutWithDrainTimeout(r, w, adapt, log, maxACPFrameDrainTime)
}

func adaptAgentStdoutWithDrainTimeout(r io.ReadCloser, w io.WriteCloser, adapt func([]byte) base.AgentAdaptResult, log *acp.EventLog, drainTimeout time.Duration) {
	defer w.Close()
	defer func() { _, _ = io.Copy(io.Discard, r) }()
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, oversized, err := readACPFrame(reader, r, drainTimeout)
		if oversized {
			log.MarkTruncated()
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return
		}
		if !oversized && len(line) > 0 {
			forward := line
			if adapt != nil {
				result := adapt(line)
				ts := time.Now().UnixMilli()
				for _, kind := range result.Log {
					log.Append(kind.Kind, ts, kind.Payload)
				}
				if result.Forward != nil {
					forward = result.Forward
				}
			}
			if len(forward) > 0 && !appendRawSessionUpdate(log, forward) {
				if _, err := w.Write(append(forward, '\n')); err != nil {
					return
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
	}
}

func appendRawSessionUpdate(log *acp.EventLog, line []byte) bool {
	return acp.AppendRawSessionUpdate(log, line)
}

const maxACPFrameBytes = 4 << 20

// The protocol is newline-framed; an oversized frame cannot retain this reader indefinitely.
const maxACPFrameDrainTime = 10 * time.Second

func readACPFrame(reader *bufio.Reader, source io.Closer, drainTimeout time.Duration) ([]byte, bool, error) {
	var line []byte
	oversized := false
	var drainTimer *time.Timer
	var drainMu sync.Mutex
	drainComplete := false
	stopDrain := func() {
		drainMu.Lock()
		drainComplete = true
		timer := drainTimer
		drainMu.Unlock()
		if timer != nil {
			timer.Stop()
		}
	}
	startDrain := func() {
		if drainTimeout <= 0 {
			_ = source.Close()
			return
		}
		drainTimer = time.AfterFunc(drainTimeout, func() {
			drainMu.Lock()
			defer drainMu.Unlock()
			if !drainComplete {
				_ = source.Close()
			}
		})
	}
	for {
		part, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(part) > maxACPFrameBytes {
				line = nil
				oversized = true
				if bytes.IndexByte(part, '\n') < 0 {
					startDrain()
				}
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			stopDrain()
			return nil, oversized, err
		}
		if oversized {
			stopDrain()
			return nil, true, err
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}
		return line, false, err
	}
}

func loadEventLogFixtureEntries(path string) ([]acp.LogEntry, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []acp.LogEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func probeHarnessAgentSessions(ctx context.Context, launch WorkerLaunch) ([]AgentSession, error) {
	ctx = WithPeerCache(ctx, "")
	target, err := harnessTarget(launch.Session.Harness)
	if err != nil {
		return nil, err
	}
	harness, err := registry.ByTarget(target)
	if err != nil {
		return nil, err
	}
	launchCtx, err := workerLaunchContext(launch)
	if err != nil {
		return nil, err
	}
	syncService := packSync.NewService()
	if _, _, syncErr := syncService.SyncForLaunchOptions(ctx, launchCtx.ProjectRoot, packSync.SyncOptions{
		Mode:          launchCtx.Mode.Name(),
		Target:        &target,
		WorkspaceRoot: launchCtx.WorkspaceRoot,
	}); syncErr != nil {
		return nil, syncErr
	}
	acpLaunch, err := harness.ACP(launchCtx)
	if err != nil {
		return nil, err
	}
	return listACPAgentSessions(ctx, launch, launchCtx, harness, acpLaunch)
}

func listACPAgentSessions(ctx context.Context, launch WorkerLaunch, launchCtx base.LaunchContext, harness base.Harness, acpLaunch base.ACPLaunch) ([]AgentSession, error) {
	cmd := acpLaunch.Cmd
	if cmd == nil {
		return nil, errors.New("harness ACP launch returned nil command")
	}
	workspace := launchCtx.WorkspaceRoot
	stdinWriter, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open ACP adapter stdin: %w", err)
	}
	agentOutR, agentOutW := io.Pipe()
	clientInR, clientInW := io.Pipe()
	cmd.Stdout = agentOutW
	cmd.Stderr = os.Stderr
	cmd.Dir = workspace
	cmd.Env = workerAgentEnvironment(launch.Environment, cmd.Env, workspace)
	cmd.Env = append(cmd.Env, "AGENTPACK_TRACE="+obs.ParseTraceParent(obs.TraceParent(ctx)).ID)

	log := acp.NewEventLog(0)
	client := acp.NewHarnessClient(workspace, log, acp.NewBroker(nil, nil, nil, nil))
	rewriter := &rewriteStdin{w: stdinWriter, rewrite: func(line []byte) []byte {
		line = acp.AddSessionCapabilities(line)
		if acpLaunch.Rewrite != nil {
			line = acpLaunch.Rewrite(line)
		}
		return line
	}}
	stdinToAgent := io.Writer(rewriter)
	clientConn := client.NewClientSideConnection(stdinToAgent, clientInR)
	go adaptAgentStdout(agentOutR, clientInW, acpLaunch.AdaptAgent, log)

	launchCtx.Command = cmd
	if err := cmd.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = agentOutR.Close()
		_ = agentOutW.Close()
		_ = clientInR.Close()
		_ = clientInW.Close()
		return nil, err
	}
	cleanup := func() {
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		_ = stdinWriter.Close()
		if rewriter != nil {
			_ = rewriter.Close()
		}
		_ = agentOutW.Close()
		_ = clientInW.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = agentOutR.Close()
		_ = clientInR.Close()
	}
	defer cleanup()
	if err := harness.AfterLaunch(launchCtx); err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := initializeACPClient(initCtx, clientConn, acpLaunch.AuthenticateMethodID); err != nil {
		return nil, err
	}
	cwd := workspace
	resp, err := clientConn.ListSessions(initCtx, sdkacp.ListSessionsRequest{Cwd: &cwd})
	if err != nil {
		return nil, err
	}
	out := make([]AgentSession, 0, len(resp.Sessions))
	for _, session := range resp.Sessions {
		title := ""
		if session.Title != nil {
			title = *session.Title
		}
		out = append(out, AgentSession{
			ID:    string(session.SessionId),
			Title: title,
			Cwd:   session.Cwd,
		})
	}
	return out, nil
}

// acpPrompter sends prompts over one ACP connection. An agent with QueuesPrompts answers each
// prompt sent during another's turn separately, so the chat's turn ends with the last answer.
type acpPrompter struct {
	log        *acp.EventLog
	sessionDir string
	harness    string

	mu      sync.Mutex
	conn    *sdkacp.ClientSideConnection
	sessID  sdkacp.SessionId
	images  bool
	handoff string        // earlier transcript for an agent taking the chat over, sent once
	sent    []*sentPrompt // prompts the agent has not answered, oldest first
}

// sentPrompt is a prompt on its way to the agent. One sent behind another stays a delivered
// queued_prompt until the agent takes it in, which ACP v1 shows only as the answer to the one
// before it (claude-agent-acp hands the turn over when the next prompt's echo arrives).
type sentPrompt struct {
	payload  json.RawMessage
	queueID  string
	inserted bool
}

func (p *acpPrompter) broker() *acp.Broker {
	return acp.NewBroker(p.prompt, p.cancel, nil, brokerSetConfigOption(&p.mu, &p.conn, &p.sessID, p.log))
}

func (p *acpPrompter) ready(setup acpSessionSetup) {
	p.mu.Lock()
	p.sessID = sdkacp.SessionId(setup.AgentSessionID)
	p.images = setup.AgentCapabilities != nil && setup.AgentCapabilities.PromptCapabilities != nil &&
		setup.AgentCapabilities.PromptCapabilities.Image
	p.mu.Unlock()
}

func (p *acpPrompter) prompt(ctx context.Context, input acp.PromptInput) (result error) {
	log := p.log
	// A store that fails is retried by the log, which tells the chat; it does not stop the prompt.
	defer func() { _ = log.Flush() }()
	p.mu.Lock()
	c, sid, images, earlier := p.conn, p.sessID, p.images, p.handoff
	p.handoff = ""
	p.mu.Unlock()
	defer func() {
		if result != nil && earlier != "" {
			p.mu.Lock()
			p.handoff = earlier
			p.mu.Unlock()
		}
	}()
	if c == nil {
		return errors.New("acp session not ready")
	}
	blocks, loggedInput, err := promptContentBlocks(p.sessionDir, input, images)
	if err != nil {
		return err
	}
	if earlier != "" {
		blocks = append([]sdkacp.ContentBlock{sdkacp.TextBlock(earlier)}, blocks...)
	}
	own := &sentPrompt{payload: promptLogPayload(loggedInput), queueID: input.QueueID}
	p.mu.Lock()
	own.inserted = len(p.sent) == 0 || own.queueID == ""
	p.sent = append(p.sent, own)
	if own.inserted {
		log.Append("prompt", time.Now().UnixMilli(), own.payload)
	} else {
		log.Append("queued_prompt", time.Now().UnixMilli(), delivered(own.payload))
	}
	p.mu.Unlock()
	_ = log.Flush()
	acp.Accepted(ctx)
	response, err := c.Prompt(ctx, sdkacp.PromptRequest{SessionId: sid, Prompt: blocks})
	p.answered(own, err, response)
	return err
}

// answered retires own: the next prompt is now taken in, and the last answer ends the turn.
// A prompt answered before it was taken in never reached the agent's conversation.
func (p *acpPrompter) answered(own *sentPrompt, err error, response sdkacp.PromptResponse) {
	log := p.log
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = slices.DeleteFunc(p.sent, func(sent *sentPrompt) bool { return sent == own })
	last := len(p.sent) == 0
	now := time.Now().UnixMilli()
	switch {
	case !own.inserted:
		reason := "cancelled"
		if err != nil {
			reason = "failed"
			logFailedTurn(log, p.harness, err, false)
		}
		log.Append("dequeued_prompt", now, mustRawJSON(map[string]string{"queueId": own.queueID, "reason": reason}))
	case err != nil:
		logFailedTurn(log, p.harness, err, last)
	}
	if !last && !p.sent[0].inserted {
		p.sent[0].inserted = true
		log.Append("prompt", now, p.sent[0].payload)
	}
	if last && err == nil {
		log.Append("turn_end", now, mustRawJSON(response))
	}
}

// delivered marks a prompt the agent has but has not taken in yet; viewers show it in the queue.
func delivered(payload json.RawMessage) json.RawMessage {
	var body map[string]any
	_ = json.Unmarshal(payload, &body)
	body["delivered"] = true
	return mustRawJSON(body)
}

// steer hands input to the agent's running turn with _session/steering, without session/cancel.
// It reports false when no turn can take it, and the caller sends it as a normal prompt.
func (p *acpPrompter) steer(ctx context.Context, input acp.PromptInput) (bool, error) {
	p.mu.Lock()
	c, sid, images := p.conn, p.sessID, p.images
	p.mu.Unlock()
	if c == nil {
		return false, errors.New("acp session not ready")
	}
	blocks, loggedInput, err := promptContentBlocks(p.sessionDir, input, images)
	if err != nil {
		return false, err
	}
	raw, err := c.CallExtension(ctx, "_session/steering", map[string]any{
		"sessionId": sid, "prompt": blocks, "_meta": map[string]any{"steering": map[string]string{"idleBehavior": "promptRequired"}},
	})
	if err != nil {
		return false, err
	}
	var result struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, err
	}
	switch result.Outcome {
	case "injected":
		p.log.Append("prompt", time.Now().UnixMilli(), promptLogPayload(loggedInput))
		return true, nil
	case "promptRequired":
		return false, nil
	}
	return false, fmt.Errorf("the agent could not take the message in (%s)", cmpOr(result.Outcome, "no outcome"))
}

func (p *acpPrompter) cancel(ctx context.Context) error {
	p.mu.Lock()
	c, sid := p.conn, p.sessID
	p.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Cancel(ctx, sdkacp.CancelNotification{SessionId: sid})
}

// A successful change records the agent's full option set, which is how the session's cached options follow it.
func brokerSetConfigOption(connMu *sync.Mutex, conn **sdkacp.ClientSideConnection, sessID *sdkacp.SessionId, log *acp.EventLog) func(context.Context, string, string) error {
	return func(ctx context.Context, key, value string) error {
		var configValue any
		if err := json.Unmarshal([]byte(value), &configValue); err != nil {
			return errors.New("invalid config value")
		}
		connMu.Lock()
		c, sid := *conn, *sessID
		connMu.Unlock()
		if c == nil {
			return errors.New("acp session not ready")
		}
		request := sdkacp.SetSessionConfigOptionRequest{}
		switch value := configValue.(type) {
		case string:
			request.ValueId = &sdkacp.SetSessionConfigOptionValueId{
				SessionId: sid,
				ConfigId:  sdkacp.SessionConfigId(key),
				Value:     sdkacp.SessionConfigValueId(value),
			}
		case bool:
			request.Boolean = &sdkacp.SetSessionConfigOptionBoolean{
				Type:      "boolean",
				SessionId: sid,
				ConfigId:  sdkacp.SessionConfigId(key),
				Value:     value,
			}
		default:
			return errors.New("config value must be a string or boolean")
		}
		response, err := c.SetSessionConfigOption(ctx, request)
		if err == nil && log != nil && response.ConfigOptions != nil {
			log.Append("config_option_update", time.Now().UnixMilli(), mustRawJSON(map[string]any{
				"sessionUpdate": "config_option_update", "configOptions": response.ConfigOptions,
			}))
		}
		return err
	}
}

func mustRawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// logFailedTurn explains a prompt the agent refused, such as one that needs a sign-in, and ends
// the turn unless another prompt still runs, so viewers see why instead of a turn that never ends.
func logFailedTurn(log *acp.EventLog, harness string, err error, end bool) {
	now := time.Now().UnixMilli()
	description := err.Error()
	if errors.Is(err, context.Canceled) || strings.Contains(description, "context canceled") || strings.Contains(description, "Request cancelled") {
		description = "The request to the agent was interrupted before it finished. Send the message again."
	}
	notice := map[string]any{"sessionUpdate": "notice", "severity": "error", "title": "The agent could not run this prompt", "description": description}
	var request *sdkacp.RequestError
	if errors.As(err, &request) && request.Code == sdkacp.NewAuthRequired(nil).Code {
		notice["code"], notice["harness"], notice["methods"] = "auth_required", harness, loginMethods(harness)
		notice["description"] = base.DisplayName(base.Target(harness)) + " needs you to sign in again."
		if harness == "opencode" {
			notice["description"] = "Sign in again or choose a model you are already signed in to."
		}
	}
	log.Append("notice", now, mustRawJSON(notice))
	if end {
		log.Append("turn_end", now, mustRawJSON(map[string]any{"stopReason": "error"}))
	}
}
