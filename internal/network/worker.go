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
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
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
	tools         chatTools
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

// CreateSessionDirectory claims id (chosen by the gateway for an instant start) or a fresh one.
func CreateSessionDirectory(root, id string) (string, error) {
	claimed := id != ""
	if id == "" {
		id = NewSessionID()
	} else if !ValidSessionID(id) {
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
	// Only an instant start arrives with its ID; other launches always get a fresh one.
	claimed := ""
	if launch.Session.ConversationState == "starting" {
		claimed = launch.Session.ID
	}
	directory, err := CreateSessionDirectory(root, claimed)
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
	validate, err := guardWorkerFolders(&launch)
	if err != nil {
		return 1, err
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
	var pty *Session
	var conversation *ConversationSession
	var acpCleanup func()
	var handler *WorkerHTTP
	defer func() {
		terminal := launch.Session.State
		if handler != nil && handler.current() != nil {
			terminal = handler.current().State()
		}
		terminal.ExitCode = code
		if err := session.EndSession(context.Background(), store.DB, launch.Session.ID, terminal); err != nil {
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
	if stored, err := session.StoredSession(ctx, store.Reader, launch.Session.ID); err != nil || stored.AgentSessionID != pending.AgentSessionID {
		pending.AgentSessionID = ""
	}
	if err := session.UpsertSession(ctx, store.DB, pending); err != nil {
		return 1, fmt.Errorf("register session: %w", err)
	}
	var server *http.Server
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), workerStopGrace)
		if handler != nil {
			_ = handler.Stop(stop)
		} else if pty != nil {
			_ = pty.Stop(stop)
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
		return StoredPromptID(ctx, store, record.ID, id)
	}
	var readyHTTP atomic.Pointer[WorkerHTTP]
	serveDone := make(chan error, 1)
	serve := func(httpHandler http.Handler) {
		server = &http.Server{Handler: httpHandler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
		obs.Go("worker.http", func() { serveDone <- server.Serve(listener) })
	}
	// An instant start's row stays "starting" until the worker serves; see Republish after ready.
	var starting atomic.Bool
	starting.Store(launch.Session.ConversationState == "starting")
	if launch.Transport == "acp" {
		prepared, err := prepareACPWorker(ctx, store, record, directory, &starting, cancelWorker, lookupPrompt, serve, &readyHTTP)
		if err != nil {
			return 1, err
		}
		launch.tools = NewChatTools(home, filepath.Base(directory))
		if launch.tools.Close != nil {
			defer launch.tools.Close()
		}
		conversation, acpCleanup, err = startACPConversation(startCtx, launch, directory, prepared)
		if err != nil {
			return 1, err
		}
		if acpCleanup != nil {
			defer func() {
				// Under the lifecycle lock, after any restart in progress: a restart that swapped the
				// agent is cleaned up here, and none can start a new one afterwards.
				if handler != nil {
					handler.lifecycle.Lock()
					defer handler.lifecycle.Unlock()
					handler.stopping = true
				}
				if acpCleanup != nil {
					acpCleanup()
					acpCleanup = nil
				}
			}()
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
	record.Session = launch.Session
	if launch.Transport == "acp" {
		record.Session.Transport = "acp"
		record.Session.AgentSessionID = launch.Session.AgentSessionID
	}
	if pty != nil {
		record.Session.State = pty.State()
	}
	if err := session.UpsertSession(ctx, store.DB, record.Session); err != nil {
		return 1, fmt.Errorf("register session: %w", err)
	}
	if server == nil {
		if err := PublishWorkerEndpoint(directory, record); err != nil {
			return 1, err
		}
	}
	wireConversation := func(current *ConversationSession) {
		wireWorkerConversation(ctx, store, record, directory, launch.Session.Goal, lookupPrompt, current)
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
			log, err := NewConversationLog(ctx, store, launch.Session.ID, cancelWorker, nil)
			if err != nil {
				return err
			}
			prepared := NewConversationSession(log, nil)
			if err := restorePendingQueue(ctx, store.Reader, record.ID, prepared); err != nil {
				return errors.Join(err, log.Close())
			}
			next, cleanup, err := startACPConversation(obs.WithRecording(ctx, handler.Diagnostics), launch, directory, prepared)
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
			if err := session.UpsertSession(ctx, store.DB, launch.Session); err != nil {
				if cleanup != nil {
					cleanup()
				}
				return err
			}
			conversation = next
			acpCleanup = cleanup
			wireConversation(next)
			handler.mu.Lock()
			handler.Conversation = next
			handler.mu.Unlock()
			next.ResumeTurns()
			return next.Log.Republish()
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
	starting.Store(false)
	if conversation != nil {
		// Through the log's writer, so the state follows every batch already stored and none overwrites a newer one.
		if err := conversation.Log.Republish(); err != nil {
			_, event := obs.Start(ctx, "worker", "persist_state")
			event.Set("session_id", record.ID)
			event.Fail(err)
			event.End()
		}
	}
	started.End()
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

// wireWorkerConversation connects a started conversation to its worker: stored prompt IDs, the
// goal saved on its row, image storage, the agent snapshot, and the permission mode last chosen
// for this agent on any chat and machine of the network.
func wireWorkerConversation(ctx context.Context, store *state.Store, record WorkerRecord, directory string, goal *session.Goal, lookupPrompt func(context.Context, string) (bool, error), current *ConversationSession) {
	permissionSettingKey := "permission_mode." + record.Session.Harness
	current.queueMu.Lock()
	current.promptStored = lookupPrompt
	current.queueMu.Unlock()
	current.restoreGoal(goal)
	current.mu.Lock()
	current.imageStore = func(data []byte) (string, error) {
		sha, err := StoreSessionBlob(directory, data)
		switch {
		case errors.Is(err, ErrSessionBlobQuota):
			return "", err
		case err != nil && SniffBlobMIME(data) == "":
			return "", errors.New("unsupported image type")
		case err != nil:
			return "", errors.New("could not store it")
		}
		// The image shows on this machine either way; peers find it once its location is recorded.
		if err := AdvertiseBlob(context.Background(), store, record.ID, sha, int64(len(data))); err != nil {
			_, event := obs.Start(ctx, "worker", "advertise_blob")
			event.Set("session_id", record.ID)
			event.Fail(err)
			event.End()
		}
		return sha, nil
	}
	current.snapshotChanged = func() { persistAgentSnapshot(ctx, store.DB, record.Session, current) }
	current.permissionChange = func(mode string) {
		if err := store.Mesh().SetSetting(context.Background(), permissionSettingKey, mode); err != nil {
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

// prepareACPWorker restores the chat's log and, under the prompt hand-off lock, publishes the
// endpoint with the requests a starting chat answers: its conversation, prompts and config.
// Once ready holds the full handler, it serves everything.
func prepareACPWorker(ctx context.Context, store *state.Store, record WorkerRecord, directory string, starting *atomic.Bool, fail func(error),
	lookupPrompt func(context.Context, string) (bool, error), serve func(http.Handler), ready *atomic.Pointer[WorkerHTTP]) (*ConversationSession, error) {
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
	prepared.starting, prepared.agentExited, prepared.promptStored = true, false, lookupPrompt
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
		case "GET /conversation", "POST /conversation/prompt", "POST /conversation/prompt/images", "POST /conversation/config":
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
