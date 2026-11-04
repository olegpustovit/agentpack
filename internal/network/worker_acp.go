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
	"strings"
	"sync"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func startACPConversation(launch WorkerLaunch, sessionDir string) (*ConversationSession, func(), error) {
	if err := EnsurePrivateDirectory(sessionDir); err != nil {
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
		log, err := loadEventLogFixture(fixture)
		if err != nil {
			return nil, nil, err
		}
		if err := log.EnablePersistence(filepath.Join(sessionDir, "conversation.ndjson")); err != nil {
			return nil, nil, err
		}
		return NewConversationSession(log, acp.NewBroker(nil, nil, nil, nil)), func() { closeACPEventLog(log) }, nil
	case "2":
		fixture := os.Getenv("AGENTPACK_TEST_ACP_FIXTURE")
		if fixture == "" {
			return nil, nil, errors.New("AGENTPACK_TEST_ACP_FIXTURE is required")
		}
		entries, err := loadEventLogFixtureEntries(fixture)
		if err != nil {
			return nil, nil, err
		}
		return startACPReplayAgent(launch, sessionDir, entries)
	}

	target, err := harnessTarget(launch.Session.Harness)
	if err != nil {
		return nil, nil, err
	}
	harness, err := registry.ByTarget(target)
	if err != nil {
		return nil, nil, err
	}
	launchCtx, err := workerLaunchContext(launch)
	if err != nil {
		return nil, nil, err
	}
	acpLaunch, err := harness.ACP(launchCtx)
	if err != nil {
		return nil, nil, err
	}
	return startACPAgentProcess(launch, launchCtx, harness, acpLaunch, sessionDir)
}

func startACPReplayAgent(launch WorkerLaunch, sessionDir string, entries []acp.LogEntry) (*ConversationSession, func(), error) {
	workspace := launch.Session.Directory
	if workspace == "" {
		workspace = launch.Directory
	}
	log := acp.NewEventLog(0)
	if err := log.EnablePersistence(filepath.Join(sessionDir, "conversation.ndjson")); err != nil {
		return nil, nil, err
	}
	var (
		connMu          sync.Mutex
		conn            *sdkacp.ClientSideConnection
		sessID          sdkacp.SessionId
		imageCapability bool
	)
	broker := acp.NewBroker(
		func(ctx context.Context, input acp.PromptInput) error {
			connMu.Lock()
			c, sid, images := conn, sessID, imageCapability
			connMu.Unlock()
			if c == nil {
				return errors.New("acp session not ready")
			}
			blocks, loggedInput, err := promptContentBlocks(sessionDir, input, images)
			if err != nil {
				return err
			}
			log.Append("prompt", time.Now().UnixMilli(), promptLogPayload(loggedInput))
			response, err := c.Prompt(ctx, sdkacp.PromptRequest{
				SessionId: sid,
				Prompt:    blocks,
			})
			if err == nil {
				log.Append("turn_end", time.Now().UnixMilli(), mustRawJSON(response))
			} else {
				logFailedTurn(log, err)
			}
			return err
		},
		func(ctx context.Context) error {
			connMu.Lock()
			c, sid := conn, sessID
			connMu.Unlock()
			if c == nil {
				return nil
			}
			return c.Cancel(ctx, sdkacp.CancelNotification{SessionId: sid})
		},
		nil,
		brokerSetConfigOption(&connMu, &conn, &sessID),
	)
	client := acp.NewHarnessClient(workspace, nil, log, broker)
	agent := acptest.NewReplayAgent(entries)
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	clientConn := client.NewClientSideConnection(c2aW, a2cR)
	agentConn := sdkacp.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	connMu.Lock()
	conn = clientConn
	connMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := func() {
		cancel()
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		_ = c2aW.Close()
		_ = a2cW.Close()
		_ = c2aR.Close()
		_ = a2cR.Close()
		_ = agentConn.Done()
		closeACPEventLog(log)
	}
	go func() { <-clientConn.Done() }()
	setup, err := initializeACPSession(ctx, clientConn, workspace, launch.Session.AgentSessionID, launch.ForkSessionID, "")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	connMu.Lock()
	sessID = sdkacp.SessionId(setup.AgentSessionID)
	imageCapability = setup.AgentCapabilities != nil &&
		setup.AgentCapabilities.PromptCapabilities != nil &&
		setup.AgentCapabilities.PromptCapabilities.Image
	connMu.Unlock()
	launch.Session.AgentSessionID = setup.AgentSessionID
	conv := NewConversationSession(log, broker)
	conv.agentSessionID = setup.AgentSessionID
	conv.SetConfigOptions(setup.ConfigOptions)
	conv.SetModes(setup.Modes)
	conv.SetAgentCapabilities(setup.AgentCapabilities)
	return conv, cleanup, nil
}

func startACPAgentProcess(launch WorkerLaunch, launchCtx base.LaunchContext, harness base.Harness, acpLaunch base.ACPLaunch, sessionDir string) (*ConversationSession, func(), error) {
	cmd := acpLaunch.Cmd
	if cmd == nil {
		return nil, nil, errors.New("harness ACP launch returned nil command")
	}
	workspace := launchCtx.WorkspaceRoot
	stdinReader, stdinWriter := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	clientInR, clientInW := io.Pipe()
	cmd.Stdin = stdinReader
	cmd.Stdout = agentOutW
	cmd.Stderr = os.Stderr
	cmd.Dir = workspace
	cmd.Env = workerAgentEnvironment(launch.Environment, cmd.Env, workspace)

	log := acp.NewEventLog(0)
	if err := log.EnablePersistence(filepath.Join(sessionDir, "conversation.ndjson")); err != nil {
		return nil, nil, err
	}
	var (
		connMu          sync.Mutex
		conn            *sdkacp.ClientSideConnection
		sessID          sdkacp.SessionId
		imageCapability bool
	)
	broker := acp.NewBroker(
		func(ctx context.Context, input acp.PromptInput) error {
			connMu.Lock()
			c, sid, images := conn, sessID, imageCapability
			connMu.Unlock()
			if c == nil {
				return errors.New("acp session not ready")
			}
			blocks, loggedInput, err := promptContentBlocks(sessionDir, input, images)
			if err != nil {
				return err
			}
			log.Append("prompt", time.Now().UnixMilli(), promptLogPayload(loggedInput))
			response, err := c.Prompt(ctx, sdkacp.PromptRequest{
				SessionId: sid,
				Prompt:    blocks,
			})
			if err == nil {
				log.Append("turn_end", time.Now().UnixMilli(), mustRawJSON(response))
			} else {
				logFailedTurn(log, err)
			}
			return err
		},
		func(ctx context.Context) error {
			connMu.Lock()
			c, sid := conn, sessID
			connMu.Unlock()
			if c == nil {
				return nil
			}
			return c.Cancel(ctx, sdkacp.CancelNotification{SessionId: sid})
		},
		nil,
		brokerSetConfigOption(&connMu, &conn, &sessID),
	)
	client := acp.NewHarnessClient(workspace, nil, log, broker)
	rewriter := &rewriteStdin{w: stdinWriter, rewrite: func(line []byte) []byte {
		line = acp.AddSessionCapabilities(line)
		if acpLaunch.Rewrite != nil {
			line = acpLaunch.Rewrite(line)
		}
		return line
	}}
	stdinToAgent := io.Writer(rewriter)
	clientConn := client.NewClientSideConnection(stdinToAgent, clientInR)
	connMu.Lock()
	conn = clientConn
	connMu.Unlock()
	adaptDone := make(chan struct{})
	go func() {
		defer close(adaptDone)
		adaptAgentStdout(agentOutR, clientInW, acpLaunch.AdaptAgent, log)
	}()

	launchCtx.Command = cmd
	if err := cmd.Start(); err != nil {
		_ = stdinReader.Close()
		_ = agentOutR.Close()
		_ = agentOutW.Close()
		_ = clientInR.Close()
		_ = clientInW.Close()
		<-adaptDone
		closeACPEventLog(log)
		return nil, nil, err
	}
	if err := harness.AfterLaunch(launchCtx); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = agentOutW.Close()
		<-adaptDone
		appendACPAgentExit(log)
		closeACPEventLog(log)
		return nil, nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	agentDone := make(chan struct{})
	cleanup := func() {
		cancel()
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		if rewriter != nil {
			_ = rewriter.Close()
		}
		_ = stdinWriter.Close()
		_ = cmd.Process.Kill()
		<-agentDone
		_ = stdinReader.Close()
		_ = agentOutR.Close()
		_ = clientInR.Close()
		closeACPEventLog(log)
	}
	go func() {
		_ = cmd.Wait()
		_ = agentOutW.Close()
		<-adaptDone
		appendACPAgentExit(log)
		cancel()
		close(agentDone)
	}()
	go func() { <-clientConn.Done() }()

	setup, err := initializeACPSession(ctx, clientConn, workspace, launch.Session.AgentSessionID, launch.ForkSessionID, acpLaunch.AuthenticateMethodID)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	connMu.Lock()
	sessID = sdkacp.SessionId(setup.AgentSessionID)
	imageCapability = setup.AgentCapabilities != nil &&
		setup.AgentCapabilities.PromptCapabilities != nil &&
		setup.AgentCapabilities.PromptCapabilities.Image
	connMu.Unlock()
	launch.Session.AgentSessionID = setup.AgentSessionID
	conv := NewConversationSession(log, broker)
	conv.agentSessionID = setup.AgentSessionID
	conv.SetConfigOptions(setup.ConfigOptions)
	conv.SetModes(setup.Modes)
	conv.SetAgentCapabilities(setup.AgentCapabilities)
	return conv, cleanup, nil
}

type acpSessionSetup struct {
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

func initializeACPSession(ctx context.Context, conn *sdkacp.ClientSideConnection, workspace, loadID, forkSessionID, authenticateMethodID string) (acpSessionSetup, error) {
	initCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if loadID != "" && forkSessionID != "" {
		return acpSessionSetup{}, errors.New("ACP session cannot be resumed and forked at the same time")
	}
	initialize, err := initializeACPClient(initCtx, conn, authenticateMethodID)
	if err != nil {
		return acpSessionSetup{}, err
	}
	setup := acpSessionSetup{AgentCapabilities: agentCapabilitiesView(initialize.AgentCapabilities)}
	if forkSessionID != "" {
		if initialize.AgentCapabilities.SessionCapabilities.Fork == nil {
			return acpSessionSetup{}, errors.New("acp agent does not advertise session.fork")
		}
		resp, err := conn.UnstableForkSession(initCtx, sdkacp.UnstableForkSessionRequest{
			SessionId:  sdkacp.SessionId(forkSessionID),
			Cwd:        workspace,
			McpServers: []sdkacp.UnstableMcpServer{},
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
			McpServers: []sdkacp.McpServer{},
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
		McpServers: []sdkacp.McpServer{},
	})
	if err != nil {
		return acpSessionSetup{}, fmt.Errorf("acp session/new: %w", err)
	}
	setup.AgentSessionID = string(resp.SessionId)
	setup.ConfigOptions = marshalConfigOptions(resp.ConfigOptions)
	setup.Modes = marshalSessionModes(resp.Modes)
	return setup, nil
}

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
func closeACPEventLog(log *acp.EventLog) {
	if err := log.Close(); err != nil {
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
	var message struct {
		Method string `json:"method"`
		Params struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &message) != nil || message.Method != "session/update" || len(message.Params.Update) == 0 {
		return false
	}
	var update struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(message.Params.Update, &update) != nil {
		return false
	}
	switch update.Kind {
	case "notice", "compaction_update", "compaction_summary_chunk":
		log.Append(update.Kind, time.Now().UnixMilli(), message.Params.Update)
		return true
	default:
		return false
	}
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

func loadEventLogFixture(path string) (*acp.EventLog, error) {
	entries, err := loadEventLogFixtureEntries(path)
	if err != nil {
		return nil, err
	}
	log := acp.NewEventLog(0)
	for _, entry := range entries {
		log.Append(entry.Kind, entry.TS, entry.Payload)
	}
	return log, nil
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
	stdinReader, stdinWriter := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	clientInR, clientInW := io.Pipe()
	cmd.Stdin = stdinReader
	cmd.Stdout = agentOutW
	cmd.Stderr = os.Stderr
	cmd.Dir = workspace
	cmd.Env = workerAgentEnvironment(launch.Environment, cmd.Env, workspace)

	log := acp.NewEventLog(0)
	client := acp.NewHarnessClient(workspace, nil, log, acp.NewBroker(nil, nil, nil, nil))
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
		return nil, err
	}
	cleanup := func() {
		if client.Terminals != nil {
			_ = client.Terminals.Close()
		}
		if rewriter != nil {
			_ = rewriter.Close()
		}
		_ = stdinWriter.Close()
		_ = agentOutW.Close()
		_ = clientInW.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdinReader.Close()
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

func brokerSetConfigOption(connMu *sync.Mutex, conn **sdkacp.ClientSideConnection, sessID *sdkacp.SessionId) func(context.Context, string, string) error {
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
		_, err := c.SetSessionConfigOption(ctx, request)
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

// logFailedTurn ends a turn the agent refused, such as one that needs a
// sign-in, so viewers see why instead of a turn that never ends.
func logFailedTurn(log *acp.EventLog, err error) {
	now := time.Now().UnixMilli()
	log.Append("notice", now, mustRawJSON(map[string]any{
		"sessionUpdate": "notice", "severity": "error", "title": "The agent could not run this prompt", "description": err.Error(),
	}))
	log.Append("turn_end", now, mustRawJSON(map[string]any{"stopReason": "error"}))
}
