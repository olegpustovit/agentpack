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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

// WorkerLaunch is delivered through a private startup pipe, never HTTP or disk.
// Launching agentpack itself here keeps staging, auth and cleanup in the CLI.
type WorkerLaunch struct {
	Session       SessionInfo `json:"session"`
	Transport     string      `json:"transport,omitempty"`
	StageInWorker bool        `json:"stage_in_worker,omitempty"`
	ForkSessionID string      `json:"fork_session_id,omitempty"`
	Program       string      `json:"program"`
	Arguments     []string    `json:"arguments"`
	Directory     string      `json:"directory"`
	Environment   []string    `json:"environment"`
	Rows          uint16      `json:"rows"`
	Columns       uint16      `json:"columns"`
	tools         chatTools
}

// WorkerRecord is private connection material, readable only by the local OS
// user. Public node listings must use a separate allowlisted session view.
type WorkerRecord struct {
	Session SessionInfo `json:"session,omitzero"`
	ID      string      `json:"id"`
	PID     int         `json:"pid"`
	Address string      `json:"address"`
	Secret  string      `json:"secret"`
}

// createSessionDirectory claims id (chosen by the gateway for an instant start) or a fresh one.
func createSessionDirectory(root, id string) (string, error) {
	claimed := id != ""
	if id == "" {
		id = newSessionID()
	} else if !validSessionID(id) {
		return "", errors.New("invalid session ID")
	}
	directory := filepath.Join(root, id)
	err := os.Mkdir(directory, 0700)
	// A resumed chat reuses its directory (it keeps the chat's images); the gateway's claim on the row
	// guarantees no other worker owns it, and an endpoint file would mean one still does.
	if errors.Is(err, fs.ErrExist) && claimed {
		if _, statErr := os.Lstat(filepath.Join(directory, "worker.json")); errors.Is(statErr, fs.ErrNotExist) {
			err = nil
		}
	}
	return directory, err
}

func newSessionID() string { return "session-" + rand.Text() }

func validSessionID(id string) bool {
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
	metadata := obs.BuildMetadata(version, localStateNode, "worker")
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
	// Only an instant start arrives with its ID; other launches always get a fresh one.
	claimed := ""
	if launch.Session.ConversationState == "starting" {
		claimed = launch.Session.ID
	}
	directory, err := createSessionDirectory(root, claimed)
	if err != nil {
		return 1, fmt.Errorf("create worker directory: %w", err)
	}
	defer func() { result = errors.Join(result, removeWorkerEndpoint(directory)) }()
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
		actor := LocalActor("local")
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
	launchIdentity, err := FolderIdentity(launch.Directory)
	if err != nil {
		return 1, err
	}
	if launch.Session.Directory != "" {
		launch.Session.FolderID, err = FolderIdentity(launch.Session.Directory)
		if err != nil {
			return 1, err
		}
	}
	validate := func() error {
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
	}
	start := func(rows, columns uint16) (*Session, error) {
		if err := validate(); err != nil {
			return nil, err
		}
		// Each launch publishes its new fingerprint after staging succeeds.
		if err := os.Remove(filepath.Join(directory, "environment")); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		command := exec.Command(launch.Program, launch.Arguments...)
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
	var session *Session
	var conversation *ConversationSession
	var acpCleanup func()
	var handler *WorkerHTTP
	defer func() {
		terminal := launch.Session.State
		if handler != nil && handler.current() != nil {
			terminal = handler.current().State()
		}
		terminal.ExitCode = code
		if err := endSession(context.Background(), store.DB, launch.Session.ID, terminal); err != nil {
			_, event := obs.Start(ctx, "worker", "persist_end")
			event.Set("session_id", launch.Session.ID)
			event.Fail(err)
			event.End()
			result = errors.Join(result, err)
		}
	}()
	if launch.Transport == "acp" {
		launch.Session.Transport = "acp"
	}
	pending := launch.Session
	// A new claim on an agent session is recorded only once its owner is verified; a resumed chat's
	// row already holds it, and keeps it so a failed start can be resumed again.
	if stored, err := storedSession(ctx, store.Reader, launch.Session.ID); err != nil || stored.AgentSessionID != pending.AgentSessionID {
		pending.AgentSessionID = ""
	}
	if err := upsertSession(ctx, store.DB, pending); err != nil {
		return 1, fmt.Errorf("register session: %w", err)
	}
	var server *http.Server
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), workerStopGrace)
		if handler != nil {
			_ = handler.Stop(stop)
		} else if session != nil {
			_ = session.Stop(stop)
		}
		cancel()
		if server != nil {
			drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(drain)
			if handler != nil {
				_ = handler.Shutdown(drain)
			}
			_ = server.Close()
		}
	}()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return 1, fmt.Errorf("generate worker secret: %w", err)
	}
	record := WorkerRecord{Session: launch.Session, ID: filepath.Base(directory), PID: os.Getpid(), Address: listener.Addr().String(), Secret: hex.EncodeToString(secret[:])}
	lookupPrompt := func(ctx context.Context, id string) (bool, error) {
		return storedPromptID(ctx, store, record.ID, id)
	}
	var readyHTTP atomic.Pointer[WorkerHTTP]
	serveDone := make(chan error, 1)
	serve := func(httpHandler http.Handler) {
		server = &http.Server{Handler: httpHandler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
		obs.Go("worker.http", func() { serveDone <- server.Serve(listener) })
	}
	if launch.Transport == "acp" {
		lock, err := lockPromptHandoff(ctx, directory)
		if err != nil {
			return 1, err
		}
		var prepared *ConversationSession
		err = func() error {
			defer lock.Close()
			log, err := newConversationLog(ctx, store, launch.Session.ID, cancelWorker)
			if err != nil {
				return err
			}
			prepared = NewConversationSession(log, nil)
			if err := restorePendingQueue(ctx, store.Reader, record.ID, prepared); err != nil {
				return errors.Join(err, log.Close())
			}
			prepared.starting, prepared.agentExited, prepared.promptStored = true, false, lookupPrompt
			bootstrap, err := WorkerHandler(ctx, nil, prepared, record.Secret, directory)
			if err != nil {
				return errors.Join(err, log.Close())
			}
			bootstrap.stateStore = store
			if err := publishWorkerEndpoint(directory, record); err != nil {
				return errors.Join(err, log.Close())
			}
			serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active := readyHTTP.Load(); active != nil {
					active.ServeHTTP(w, r)
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /conversation", "POST /conversation/prompt", "POST /conversation/prompt/images", "POST /conversation/config":
					bootstrap.ServeHTTP(w, r)
				default:
					http.Error(w, "agent is starting", http.StatusServiceUnavailable)
				}
			}))
			return nil
		}()
		if err != nil {
			return 1, err
		}
		launch.tools = newChatTools(home, filepath.Base(directory))
		if launch.tools.close != nil {
			defer launch.tools.close()
		}
		conversation, acpCleanup, err = startACPConversation(startCtx, launch, directory, prepared)
		if err != nil {
			return 1, err
		}
		if acpCleanup != nil {
			defer func() {
				if acpCleanup != nil {
					acpCleanup()
				}
			}()
		}
		// Restart must load the forked ID rather than fork its source again.
		launch.ForkSessionID = ""
		launch.Session.AgentSessionID = conversation.agentSessionID
		launch.Session.AgentCapabilities = conversation.AgentCapabilitiesView()
		// Other gateways read this replicated row to decide whether the chat is live.
		launch.Session.State = TerminalState{Running: !conversation.AgentExited(), ExitCode: -1}
		if err := rememberACPConversationOwner(ctx, store, launch.Session, launch.Directory); err != nil {
			return 1, fmt.Errorf("persist ACP session owner: %w", err)
		}
	} else {
		var err error
		session, err = start(launch.Rows, launch.Columns)
		if err != nil {
			return 1, err
		}
	}
	record.Session = launch.Session
	// An instant start stays "starting" in the row until the endpoint serves; see the patch after ready.
	instant := launch.Session.ConversationState == "starting"
	if launch.Transport == "acp" {
		record.Session.Transport = "acp"
		record.Session.AgentSessionID = launch.Session.AgentSessionID
		if conversation != nil {
			record.Session.ConversationState = conversation.ConversationState()
		}
	}
	liveState := record.Session.ConversationState
	if instant {
		record.Session.ConversationState = "starting"
	}
	if session != nil {
		record.Session.State = session.State()
	}
	if err := upsertSession(ctx, store.DB, record.Session); err != nil {
		return 1, fmt.Errorf("register session: %w", err)
	}
	if server == nil {
		if err := publishWorkerEndpoint(directory, record); err != nil {
			return 1, err
		}
	}
	var stateWriteWarning sync.Once
	persistConversationState := func(current *ConversationSession) func(string) {
		return func(state string) {
			var err error
			if state != "working" {
				err = current.log.Flush()
			}
			if err == nil {
				err = patchWorkerConversationState(context.Background(), store.DB, record.ID, state)
			}
			if err != nil {
				if err == errSessionArchived {
					cancelWorker(err)
					return
				}
				stateWriteWarning.Do(func() {
					_, event := obs.Start(ctx, "worker", "persist_state")
					event.Set("session_id", record.ID)
					event.Fail(err)
					event.End()
					fmt.Fprintln(os.Stderr, "warning: ACP worker state could not be persisted")
				})
			}
		}
	}
	persistTitle := func(title string) {
		if _, err := store.DB.ExecContext(context.Background(), "UPDATE sessions SET title=? WHERE id=? AND archived_at=0 AND node_id="+state.LocalNodeSQL, title, record.ID); err != nil {
			_, event := obs.Start(ctx, "worker", "set_title")
			event.Set("session_id", record.ID)
			event.Fail(err)
			event.End()
		}
	}
	// The permission mode last chosen for this agent, on any chat and machine of the network.
	permissionSettingKey := "permission_mode." + launch.Session.Harness
	wireConversation := func(current *ConversationSession) {
		current.queueMu.Lock()
		current.promptStored = lookupPrompt
		current.queueMu.Unlock()
		current.restoreGoal(launch.Session.Goal)
		current.mu.Lock()
		current.titleChange = persistTitle
		current.snapshotChanged = func() { persistAgentSnapshot(ctx, store.DB, record.Session, current) }
		current.goalChange = func(goal *Goal) {
			persistSessionJSON(context.Background(), store.DB, record.ID, "goal", goal, goal == nil)
		}
		current.subagentsChange = func(subagents []SubagentSummary) {
			persistSessionJSON(context.Background(), store.DB, record.ID, "subagents", subagents, len(subagents) == 0)
		}
		current.permissionChange = func(mode string) {
			if _, err := store.DB.ExecContext(context.Background(), "INSERT INTO mesh_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", permissionSettingKey, mode); err != nil {
				_, event := obs.Start(ctx, "worker", "remember_permissions")
				event.Fail(err)
				event.End()
			}
		}
		current.mu.Unlock()
		current.applyRememberedPermissions(ctx, store, permissionSettingKey)
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
	if conversation != nil {
		conversation.configMu.Lock()
		wireConversation(conversation)
		conversation.SetStateChange(liveState, persistConversationState(conversation))
		conversation.ResumeTurns()
		conversation.configMu.Unlock()
	}
	handler, err = WorkerHandler(ctx, session, conversation, record.Secret, directory)
	if err != nil {
		return 1, err
	}
	handler.stateStore = store
	if session != nil {
		handler.start = start
		handler.validate = validate
	} else if conversation != nil {
		handler.validate = validate
		handler.stopACP = func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if acpCleanup != nil {
				acpCleanup()
				acpCleanup = nil
			}
			return nil
		}
		handler.restartACP = func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if acpCleanup != nil {
				acpCleanup()
				acpCleanup = nil
			}
			log, err := newConversationLog(ctx, store, launch.Session.ID, cancelWorker)
			if err != nil {
				return err
			}
			prepared := NewConversationSession(log, nil)
			if err := restorePendingQueue(ctx, store.Reader, record.ID, prepared); err != nil {
				return errors.Join(err, log.Close())
			}
			next, cleanup, err := startACPConversation(obs.WithRecording(ctx, handler.diagnostics), launch, directory, prepared)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				if cleanup != nil {
					cleanup()
				}
				return err
			}
			launch.Session.AgentSessionID = next.agentSessionID
			launch.Session.AgentCapabilities = next.AgentCapabilitiesView()
			launch.Session.ConversationState = next.ConversationState()
			launch.Session.State.Running = !next.AgentExited()
			if err := upsertSession(ctx, store.DB, launch.Session); err != nil {
				if cleanup != nil {
					cleanup()
				}
				return err
			}
			conversation = next
			acpCleanup = cleanup
			wireConversation(next)
			handler.mu.Lock()
			handler.conversation = next
			handler.mu.Unlock()
			next.SetStateChange(next.ConversationState(), persistConversationState(next))
			next.ResumeTurns()
			return nil
		}
	}
	if server == nil {
		serve(handler)
	} else {
		readyHTTP.Store(handler)
	}
	if err := ready(record.ID); err != nil {
		return 1, fmt.Errorf("announce worker: %w", err)
	}
	if instant && conversation != nil {
		persistConversationState(conversation)(conversation.ConversationState())
	}
	started.End()
	select {
	case <-ctx.Done():
		if context.Cause(ctx) == errSessionArchived {
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

func publishWorkerEndpoint(directory string, record WorkerRecord) error {
	endpoint := record
	endpoint.Session = SessionInfo{}
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
