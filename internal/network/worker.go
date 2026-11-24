package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// WorkerLaunch is delivered through a private startup pipe, never HTTP or disk.
// Launching agentpack itself here keeps staging, auth and cleanup in the CLI.
type WorkerLaunch struct {
	Session       session.SessionInfo `json:"session"`
	Transport     string              `json:"transport,omitempty"`
	StageInWorker bool                `json:"stage_in_worker,omitempty"`
	ForkSessionID string              `json:"fork_session_id,omitempty"`
	Program       string              `json:"program"`
	Arguments     []string            `json:"arguments"`
	Directory     string              `json:"directory"`
	Environment   []string            `json:"environment"`
	Rows          uint16              `json:"rows"`
	Columns       uint16              `json:"columns"`

	tools             chatTools
	replaceMissingACP bool
}

// WorkerRecord is private connection material, readable only by the local OS
// user. Public node listings must use a separate allowlisted session view.
type WorkerRecord struct {
	Session session.SessionInfo `json:"session,omitzero"`
	ID      string              `json:"id"`
	PID     int                 `json:"pid"`
	Address string              `json:"address"`
	Secret  string              `json:"secret"`
}

// CreateSessionDirectory claims id or a fresh one. Only chat resumes can reuse a stopped worker directory.
func CreateSessionDirectory(root, id string, reuse bool) (string, error) {
	reuse = reuse && id != ""
	if id == "" {
		id = NewSessionID()
	} else if !ValidSessionID(id) {
		return "", errors.New("invalid session ID")
	}
	directory := filepath.Join(root, id)
	err := os.Mkdir(directory, 0700)
	// A resumed chat reuses its directory (it keeps the chat's images); the gateway's claim on the row
	// guarantees no other worker owns it, and an endpoint file would mean one still does.
	if errors.Is(err, fs.ErrExist) && reuse {
		if _, statErr := os.Lstat(filepath.Join(directory, "worker.json")); errors.Is(statErr, fs.ErrNotExist) {
			err = nil
		}
	}
	return directory, err
}

func NewSessionID() string { return "session-" + rand.Text() }

func ValidSessionID(id string) bool {
	return strings.HasPrefix(id, "session-") && filepath.Base(id) == id && !strings.ContainsAny(id, "/\\")
}

// RunWorker owns a restartable PTY independently of a gateway. ready receives
// only the non-secret worker ID, which survives supervised child restarts.
func RunWorker(ctx context.Context, home, version string, launch WorkerLaunch, ready func(string) error) (code int, result error) {
	ctx, cancelWorker := context.WithCancelCause(ctx)
	defer cancelWorker(nil)
	spool, err := obs.OpenSpool(home, obs.SpoolOptions{Proc: "worker"})
	if err != nil {
		return 1, err
	}
	defer func() { result = errors.Join(result, spool.Close()) }()
	metadata := obs.BuildMetadata(version, LocalStateNode, "worker")
	ctx = obs.WithSink(obs.ProcessTrace(ctx), spool, metadata)
	restore := obs.SetDefault(spool, metadata)
	defer restore()
	startCtx, started := obs.Start(ctx, "worker", "start")
	started.Set("harness", launch.Session.Harness)
	defer func() {
		if started.TS.IsZero() {
			started.Status = code
			started.Fail(result)
			started.End()
		}
	}()
	if !filepath.IsAbs(launch.Program) || !filepath.IsAbs(launch.Directory) {
		return 1, errors.New("worker program and directory must be absolute")
	}
	root := filepath.Join(home, "network", "sessions")
	if err := paths.EnsurePrivateDirectory(filepath.Dir(root)); err != nil {
		return 1, fmt.Errorf("protect worker network directory: %w", err)
	}
	if err := paths.EnsurePrivateDirectory(root); err != nil {
		return 1, fmt.Errorf("create worker root: %w", err)
	}
	// A chat claim may resume an existing directory. A claimed terminal needs a fresh directory.
	claimed := ""
	reuse := launch.Session.ConversationState == "starting"
	if reuse || launch.Transport == "terminal" {
		claimed = launch.Session.ID
	}
	directory, err := CreateSessionDirectory(root, claimed, reuse)
	if err != nil {
		return 1, fmt.Errorf("create worker directory: %w", err)
	}
	defer func() { result = errors.Join(result, removeWorkerEndpoint(directory)) }()
	// A fresh agent starts with the current environment, so no earlier reload is still due.
	if err := os.Remove(filepath.Join(directory, "restart_pending")); err != nil && !os.IsNotExist(err) {
		return 1, err
	}
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	nodeID, err := store.NodeID(ctx)
	if err != nil {
		return 1, err
	}
	metadata = obs.BuildMetadata(version, nodeID, "worker")
	ctx = obs.WithSink(ctx, spool, metadata)
	startCtx = obs.WithSink(startCtx, spool, metadata)
	started.NodeID = nodeID
	restoreNode := obs.SetDefault(spool, metadata)
	defer restoreNode()
	launch.Session.ID = filepath.Base(directory)
	launch.Session.LastUsed = time.Now().UTC()
	if launch.Session.StartedAt.IsZero() {
		launch.Session.StartedAt = launch.Session.LastUsed
	}
	if launch.Session.StartedBy == nil {
		actor := session.LocalActor("local")
		launch.Session.StartedBy = &actor
	}
	started.Set("session_id", filepath.Base(directory))
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return 1, fmt.Errorf("protect worker directory: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("listen for worker attachments: %w", err)
	}
	defer listener.Close()
	worker := workerRuntime{home: home, directory: directory, store: store, launch: launch, listener: listener, cancel: cancelWorker}
	return worker.run(ctx, startCtx, started, ready)
}

type workerRuntime struct {
	home, directory string
	store           *state.Store
	launch          WorkerLaunch
	listener        net.Listener
	cancel          context.CancelCauseFunc
}

func (worker *workerRuntime) run(ctx, startCtx context.Context, started *obs.Event, ready func(string) error) (code int, result error) {
	launch := &worker.launch
	store, directory := worker.store, worker.directory
	validate, err := guardWorkerFolders(launch)
	if err != nil {
		return 1, err
	}
	// agentSession is the terminal agent's own session, which a restart resumes; empty at first launch.
	agentSession := func() string { return "" }
	start := func(rows, columns uint16) (*Session, error) {
		return worker.startTerminal(ctx, validate, agentSession, rows, columns)
	}
	var pty *Session
	var conversation *ConversationSession
	var agent *acpAgent
	var handler *WorkerHTTP
	var server *http.Server
	defer func() {
		worker.finish(ctx, server, handler, pty, code, &result)
	}()

	record, err := worker.sessionRecord(ctx)
	if err != nil {
		return 1, err
	}
	var readyHTTP atomic.Pointer[WorkerHTTP]
	serveDone := make(chan error, 1)
	serve := func(httpHandler http.Handler) {
		server = &http.Server{Handler: httpHandler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
		obs.Go("worker.http", func() { serveDone <- server.Serve(worker.listener) })
	}
	// An instant start's row stays "starting" until the worker serves; see Republish after ready.
	var starting atomic.Bool
	starting.Store(launch.Session.ConversationState == "starting")
	if launch.Transport == "acp" {
		agent = &acpAgent{home: worker.home, store: store, launch: launch, record: record, directory: directory, cancel: worker.cancel,
			createdNativeSession: launch.Session.AgentSessionID == "" && launch.ForkSessionID == ""}
		prepared, err := prepareACPWorker(ctx, store, record, directory, &starting, worker.cancel, serve, &readyHTTP)
		if err != nil {
			return 1, err
		}
		launch.tools = NewChatTools(worker.home, filepath.Base(directory))
		if launch.tools.Close != nil {
			defer launch.tools.Close()
		}
		conversation, agent.cleanup, err = startACPConversation(startCtx, *launch, directory, prepared)
		if err != nil {
			return 1, err
		}
		agent.recordInitialEnvironment(ctx)
		if agent.cleanup != nil {
			defer agent.closeForShutdown()
		}
		// Restart must load the forked ID rather than fork its source again.
		launch.ForkSessionID = ""
		launch.Session.AgentSessionID = conversation.agentSessionID
		launch.Session.AgentCapabilities = conversation.AgentCapabilitiesView()
		// Other gateways read this replicated row to decide whether the chat is live.
		launch.Session.State = session.TerminalState{Running: !conversation.AgentExited(), ExitCode: -1}
		if err := RememberACPConversationOwner(ctx, store, launch.Session, launch.Directory); err != nil {
			return 1, fmt.Errorf("persist ACP session owner: %w", err)
		}
	} else {
		var err error
		pty, err = start(launch.Rows, launch.Columns)
		if err != nil {
			return 1, err
		}
	}
	if err := worker.publishSession(ctx, &record, pty, server == nil); err != nil {
		return 1, err
	}
	wireConversation := func(current *ConversationSession) {
		wireWorkerConversation(ctx, store, record, directory, launch.Session.Goal, current)
	}
	if conversation != nil {
		conversation.configMu.Lock()
		wireConversation(conversation)
		conversation.ResumeTurns()
		conversation.configMu.Unlock()
	}
	handler, err = WorkerHandler(ctx, pty, conversation, record.Secret, directory)
	if err != nil {
		return 1, err
	}
	handler.StateStore = store
	if pty != nil {
		handler.Start = start
		handler.validate = validate
		agentSession = func() string { id, _ := handler.agentSession.Load().(string); return id }
	} else if conversation != nil {
		if environment, ok := agent.environment(); ok {
			handler.environment = &environment
		}
		handler.validate = validate
		agent.handler = handler
		agent.wire = wireConversation
		handler.stopACP = agent.stop
		handler.restartACP = agent.restart
	}
	if server == nil {
		serve(handler)
	} else {
		readyHTTP.Store(handler)
	}
	if err := ready(record.ID); err != nil {
		return 1, fmt.Errorf("announce worker: %w", err)
	}
	starting.Store(false)
	if agent != nil {
		conversation = agent.current()
	}
	republishWorkerState(ctx, record.ID, conversation)
	started.End()
	return waitForWorker(ctx, handler, serveDone)
}

func (worker *workerRuntime) startTerminal(ctx context.Context, validate func() error, agentSession func() string, rows, columns uint16) (*Session, error) {
	launch, directory := &worker.launch, worker.directory
	if err := validate(); err != nil {
		return nil, err
	}
	// Each launch publishes its new fingerprint after staging succeeds.
	if err := os.Remove(filepath.Join(directory, "environment")); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	command := exec.Command(launch.Program, resumeArguments(*launch, agentSession)...)
	environment := launch.Environment
	if environment == nil {
		environment = os.Environ()
	}
	command.Dir, command.Env = launch.Directory, append(append([]string(nil), environment...), "AGENTPACK_SESSION=1", "AGENTPACK_SESSION_ID="+filepath.Base(directory))
	command.Env = append(command.Env, "AGENTPACK_TRACE="+obs.ParseTraceParent(obs.TraceParent(ctx)).ID)
	session, err := StartSession(command, rows, columns)
	if session != nil {
		session.shell = launch.Transport == "shell"
	}
	return session, err
}

func (worker *workerRuntime) sessionRecord(ctx context.Context) (WorkerRecord, error) {
	launch, store := &worker.launch, worker.store
	if launch.Transport == "acp" {
		launch.Session.Transport = "acp"
	}
	pending := launch.Session
	// A new claim on an agent session is recorded only once its owner is verified; a resumed chat's
	// row already holds it, and keeps it so a failed start can be resumed again.
	if stored, err := session.StoredSession(ctx, store.Reader, launch.Session.ID); err != nil || stored.AgentSessionID != pending.AgentSessionID {
		pending.AgentSessionID = ""
	}
	if err := session.UpsertSession(ctx, store.DB, pending); err != nil {
		return WorkerRecord{}, fmt.Errorf("register session: %w", err)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return WorkerRecord{}, fmt.Errorf("generate worker secret: %w", err)
	}
	return WorkerRecord{Session: launch.Session, ID: filepath.Base(worker.directory), PID: os.Getpid(), Address: worker.listener.Addr().String(), Secret: hex.EncodeToString(secret[:])}, nil
}

func (worker *workerRuntime) publishSession(ctx context.Context, record *WorkerRecord, pty *Session, publishEndpoint bool) error {
	launch := &worker.launch
	record.Session = launch.Session
	if launch.Transport == "acp" {
		record.Session.Transport = "acp"
		record.Session.AgentSessionID = launch.Session.AgentSessionID
	}
	if pty != nil {
		record.Session.State = pty.State()
		record.Session.LastUsed = time.Now().UTC()
	}
	if err := session.UpsertSession(ctx, worker.store.DB, record.Session); err != nil {
		return fmt.Errorf("register session: %w", err)
	}
	if publishEndpoint {
		return PublishWorkerEndpoint(worker.directory, *record)
	}
	return nil
}

func republishWorkerState(ctx context.Context, id string, conversation *ConversationSession) {
	if conversation != nil {
		// Through the log's writer, so the state follows every batch already stored and none overwrites a newer one.
		if err := conversation.Log.Republish(); err != nil {
			_, event := obs.Start(ctx, "worker", "persist_state")
			event.Set("session_id", id)
			event.Fail(err)
			event.End()
		}
	}
}

func waitForWorker(ctx context.Context, handler *WorkerHTTP, serveDone <-chan error) (int, error) {
	select {
	case <-ctx.Done():
		if context.Cause(ctx) == session.ErrSessionArchived {
			return 0, nil
		}
		return 1, context.Cause(ctx)
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return 1, fmt.Errorf("serve worker: %w", err)
		}
		return 0, nil
	case <-handler.done:
		if handler.current() == nil {
			return 0, handler.err
		}
		// A short-lived CLI child can finish before its launcher reads the record.
		// Retain the endpoint until that first attachment can collect final output.
		select {
		case <-handler.firstAttachment():
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		code, err := handler.current().Wait(context.Background())
		return code, errors.Join(err, handler.err)
	}
}

func (worker *workerRuntime) finish(ctx context.Context, server *http.Server, handler *WorkerHTTP, pty *Session, code int, result *error) {
	stop, cancel := context.WithTimeout(context.Background(), workerStopGrace)
	if handler != nil {
		_ = handler.Stop(stop)
	} else if pty != nil {
		_ = pty.Stop(stop)
	}
	cancel()
	// Keep answering the liveness probe until the worker's final state is stored.
	terminal := worker.launch.Session.State
	if handler != nil && handler.current() != nil {
		terminal = handler.current().State()
	}
	terminal.ExitCode = code
	if err := session.EndSession(context.Background(), worker.store.DB, worker.launch.Session.ID, terminal); err != nil {
		_, event := obs.Start(ctx, "worker", "persist_end")
		event.Set("session_id", worker.launch.Session.ID)
		event.Fail(err)
		event.End()
		*result = errors.Join(*result, err)
	}
	if server != nil {
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(drain)
		if handler != nil {
			_ = handler.Shutdown(drain)
		}
		_ = server.Close()
	}
}

func PublishWorkerEndpoint(directory string, record WorkerRecord) error {
	endpoint := record
	endpoint.Session = session.SessionInfo{}
	data, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	// Publish a complete record; node discovery ignores temporary files.
	temporary := filepath.Join(directory, "worker.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write worker record: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(directory, "worker.json")); err != nil {
		return fmt.Errorf("publish worker record: %w", err)
	}
	return nil
}

// wireWorkerConversation connects a started conversation to its worker: its persistence adapter,
// the goal saved on its row, and the permission mode last chosen for this agent on any chat and
// machine of the network.
func wireWorkerConversation(ctx context.Context, store *state.Store, record WorkerRecord, directory string, goal *session.Goal, current *ConversationSession) {
	current.wireStore(ctx, store, record.Session, directory)
	current.restoreGoal(goal)
	current.applyRememberedPermissions(ctx, store, record.Session.Harness)
	wanted, err := savedConfig(ctx, store, record.Session)
	if err != nil {
		_, event := obs.Start(ctx, "worker", "load_saved_config")
		event.Set("session_id", record.ID)
		event.Fail(err)
		event.End()
	} else {
		current.applyWantedConfig(ctx, wanted)
	}
	current.logConfiguration()
}

// prepareACPWorker restores the chat's log and, under the prompt hand-off lock, publishes the
// endpoint with the requests a starting chat answers: its conversation, prompts and config.
// Once ready holds the full handler, it serves everything.
func prepareACPWorker(ctx context.Context, store *state.Store, record WorkerRecord, directory string, starting *atomic.Bool, fail func(error),
	serve func(http.Handler), ready *atomic.Pointer[WorkerHTTP]) (*ConversationSession, error) {
	lock, err := LockPromptHandoff(ctx, directory)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	log, err := NewConversationLog(ctx, store, record.ID, fail, starting)
	if err != nil {
		return nil, err
	}
	prepared := NewConversationSession(log, nil)
	if err := restorePendingQueue(ctx, store.Reader, record.ID, prepared); err != nil {
		return nil, errors.Join(err, log.Close())
	}
	prepared.starting, prepared.agentExited = true, false
	prepared.wireStore(ctx, store, record.Session, directory)
	bootstrap, err := WorkerHandler(ctx, nil, prepared, record.Secret, directory)
	if err != nil {
		return nil, errors.Join(err, log.Close())
	}
	bootstrap.StateStore = store
	if err := PublishWorkerEndpoint(directory, record); err != nil {
		return nil, errors.Join(err, log.Close())
	}
	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active := ready.Load(); active != nil {
			active.ServeHTTP(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /state", "GET /conversation", "POST /conversation/prompt", "POST /conversation/prompt/images", "POST /conversation/config":
			bootstrap.ServeHTTP(w, r)
		default:
			http.Error(w, "agent is starting", http.StatusServiceUnavailable)
		}
	}))
	return prepared, nil
}

// guardWorkerFolders records the launch's folder identities (the workspace's on the session) and
// returns the check that both are still the folders the launch was made for.
func guardWorkerFolders(launch *WorkerLaunch) (func() error, error) {
	launchIdentity, err := FolderIdentity(launch.Directory)
	if err != nil {
		return nil, err
	}
	if launch.Session.Directory != "" {
		identity, err := FolderIdentity(launch.Session.Directory)
		if err != nil {
			return nil, err
		}
		if launch.Session.FolderID != "" && !SameFolder(launch.Session.FolderID, identity) {
			return nil, errors.New("workspace has changed before launch")
		}
		launch.Session.FolderID = identity
	}
	return func() error {
		identity, err := FolderIdentity(launch.Directory)
		if err != nil || identity != launchIdentity {
			return errors.New("launch folder has changed; launch the agent locally again")
		}
		if launch.Session.Directory != "" {
			identity, err = FolderIdentity(launch.Session.Directory)
			if err != nil || identity != launch.Session.FolderID {
				return errors.New("workspace has changed; launch the agent locally again")
			}
		}
		return nil
	}, nil
}

// Error spools written before opening state are attributed by the local gateway.
const LocalStateNode = "local"

// acpAgent owns a worker's running ACP agent process: it stops it, and on restart starts a new one
// on a fresh conversation log and swaps it into the worker's handler.
type acpAgent struct {
	home      string
	store     *state.Store
	launch    *WorkerLaunch
	record    WorkerRecord
	directory string
	cancel    context.CancelCauseFunc
	handler   *WorkerHTTP
	wire      func(*ConversationSession)
	cleanup   func()

	createdNativeSession bool
}

func (agent *acpAgent) recordInitialEnvironment(ctx context.Context) {
	// What this agent launched with, the base later changes are compared with; off the start path.
	if environment, ok := agent.environment(); ok {
		obs.Go("worker.record_environment", func() {
			if _, _, err := environment.record(); err != nil {
				_, event := obs.Start(ctx, "worker", "record_environment")
				event.Set("session_id", agent.record.ID)
				event.Fail(err)
				event.End()
			}
		})
	}
}

func (agent *acpAgent) closeForShutdown() {
	// Under the lifecycle lock, after any restart in progress: a restart that swapped the
	// agent is cleaned up here, and none can start a new one afterwards.
	if handler := agent.handler; handler != nil {
		handler.lifecycle.Lock()
		defer handler.lifecycle.Unlock()
		handler.stopping = true
	}
	agent.close()
}

// close stops the agent process, once.
func (agent *acpAgent) close() {
	if agent.cleanup != nil {
		agent.cleanup()
		agent.cleanup = nil
	}
}

func (agent *acpAgent) current() *ConversationSession {
	agent.handler.mu.Lock()
	defer agent.handler.mu.Unlock()
	return agent.handler.Conversation
}

func (agent *acpAgent) stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	agent.close()
	return nil
}

// environment is what the chat's agent runs with, for recording and checking it; false in the
// ACP fixture modes, which have no project.
func (agent *acpAgent) environment() (launchEnvironment, bool) {
	if os.Getenv("AGENTPACK_TEST_ACP") != "" {
		return launchEnvironment{}, false
	}
	launchCtx, err := workerLaunchContext(*agent.launch)
	if err != nil {
		return launchEnvironment{}, false
	}
	target, err := HarnessTarget(agent.launch.Session.Harness)
	if err != nil {
		return launchEnvironment{}, false
	}
	return launchEnvironment{home: agent.home, id: agent.record.ID, definition: launchCtx.ProjectRoot, workspace: launchCtx.WorkspaceRoot, mode: launchCtx.Mode.Name(), target: target}, true
}

// restart replaces the agent with one started on the current environment, which resumes the same
// conversation. When that fails the chat stays open on a log that says why (see hold).
func (agent *acpAgent) restart(ctx context.Context) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	environment, recordable := agent.environment()
	launch, store := agent.launch, agent.store
	if recordable && !launch.StageInWorker {
		// The worker stages only an agent that stages at session start; restage the others here.
		launchCtx, err := workerLaunchContext(*launch)
		if err != nil {
			return err
		}
		if _, _, err := packSync.NewService().SyncForLaunchOptions(ctx, launchCtx.ProjectRoot, packSync.SyncOptions{
			Mode: launchCtx.Mode.Name(), Target: &environment.target, WorkspaceRoot: launchCtx.WorkspaceRoot,
		}); err != nil {
			appendReloadFailed(agent.current().Log, err) // the running agent keeps the chat
			return err
		}
	}
	agent.close()
	defer func() {
		if result != nil {
			agent.hold(ctx, result)
		}
	}()
	reload := *launch
	if agent.createdNativeSession && launch.Session.AgentSessionID != "" {
		// An adapter may not save its session until the first prompt. Only replace a missing ID
		// when this worker created it and the durable chat has no messages to resume.
		err := store.Reader.QueryRowContext(ctx, `SELECT conversation_truncated=0 AND NOT EXISTS (
			SELECT 1 FROM conversation_events WHERE session_id=sessions.id
			AND kind IN ('prompt','user_prompt','user_message_chunk','agent_message_chunk')
		) FROM sessions WHERE id=?`, launch.Session.ID).Scan(&reload.replaceMissingACP)
		if err != nil {
			return fmt.Errorf("check chat history before reload: %w", err)
		}
	}
	log, err := NewConversationLog(ctx, store, launch.Session.ID, agent.cancel, nil)
	if err != nil {
		return err
	}
	prepared := NewConversationSession(log, nil)
	if err := restorePendingQueue(ctx, store.Reader, agent.record.ID, prepared); err != nil {
		return errors.Join(err, log.Close())
	}
	next, cleanup, err := startACPConversation(obs.WithRecording(ctx, agent.handler.Diagnostics), reload, agent.directory, prepared)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return err
	}
	info := launch.Session
	info.AgentSessionID = next.agentSessionID
	info.AgentCapabilities = next.AgentCapabilitiesView()
	info.ConversationState = next.ConversationState()
	info.State.Running = !next.AgentExited()
	if info.AgentSessionID != launch.Session.AgentSessionID {
		if err := RememberACPConversationOwner(ctx, store, info, launch.Directory); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return fmt.Errorf("persist replacement ACP session owner: %w", err)
		}
	}
	if err := session.UpsertSession(ctx, store.DB, info); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return err
	}
	launch.Session = info
	agent.cleanup = cleanup
	agent.wire(next)
	agent.handler.mu.Lock()
	agent.handler.Conversation = next
	agent.handler.mu.Unlock()
	if recordable {
		if previous, current, err := environment.record(); err != nil {
			_, event := obs.Start(ctx, "worker", "record_environment")
			event.Set("session_id", agent.record.ID)
			event.Fail(err)
			event.End()
		} else if previous.Digest != "" && previous.Digest != current.Digest {
			next.Log.Append("notice", time.Now().UnixMilli(), MustRawJSON(map[string]string{
				"sessionUpdate": "notice", "severity": "info", "title": reloadNotice(previous.Items, current.Items),
			}))
		}
	}
	next.ResumeTurns()
	return next.Log.Republish()
}

// hold keeps a chat whose reload failed open: its log says why, and its next prompt retries the
// reload. Only when even that log cannot open does the worker end, as before.
func (agent *acpAgent) hold(ctx context.Context, cause error) {
	ctx = context.WithoutCancel(ctx)
	log, err := NewConversationLog(ctx, agent.store, agent.launch.Session.ID, agent.cancel, nil)
	if err != nil {
		agent.cancel(errors.Join(cause, err))
		return
	}
	held := NewConversationSession(log, nil)
	held.mu.Lock()
	held.agentExited, held.sessionID = true, agent.record.ID
	held.mu.Unlock()
	appendReloadFailed(log, cause)
	agent.cleanup = func() { closeACPEventLog(log) }
	agent.handler.mu.Lock()
	agent.handler.Conversation = held
	agent.handler.mu.Unlock()
}

func appendReloadFailed(log *acp.EventLog, cause error) {
	log.Append("notice", time.Now().UnixMilli(), MustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "error", "title": "Couldn't reload", "description": cause.Error() + ". Send a message to retry.",
	}))
}

// resumeArguments relaunch a terminal agent on its own session when its harness resumes by ID.
func resumeArguments(launch WorkerLaunch, agentSession func() string) []string {
	id := agentSession()
	if id == "" || launch.Transport == "shell" {
		return launch.Arguments
	}
	target, err := HarnessTarget(launch.Session.Harness)
	if err != nil {
		return launch.Arguments
	}
	entry, err := registry.ByTarget(target)
	if err != nil || entry.ResumeArgs == nil {
		return launch.Arguments
	}
	return append(slices.Clone(launch.Arguments), entry.ResumeArgs(id)...)
}
