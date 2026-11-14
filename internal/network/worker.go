package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
	ForkSessionID string      `json:"fork_session_id,omitempty"`
	Program       string      `json:"program"`
	Arguments     []string    `json:"arguments"`
	Directory     string      `json:"directory"`
	Environment   []string    `json:"environment"`
	Rows          uint16      `json:"rows"`
	Columns       uint16      `json:"columns"`
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

func createSessionDirectory(root string) (string, error) {
	directory := filepath.Join(root, "session-"+rand.Text())
	return directory, os.Mkdir(directory, 0700)
}

// RunWorker owns a restartable PTY independently of a gateway. ready receives
// only the non-secret worker ID, which survives supervised child restarts.
func RunWorker(ctx context.Context, home, version string, launch WorkerLaunch, ready func(string) error) (code int, result error) {
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
	directory, err := createSessionDirectory(root)
	if err != nil {
		return 1, fmt.Errorf("create worker directory: %w", err)
	}
	defer os.RemoveAll(directory)
	store, err := state.Open(home)
	if err != nil {
		return 1, err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
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
		return StartSession(command, rows, columns)
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
		var err error
		conversation, acpCleanup, err = startACPConversation(startCtx, launch, directory)
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
		if err := rememberACPConversationOwner(home, launch.Session, launch.Directory); err != nil {
			return 1, fmt.Errorf("persist ACP session owner: %w", err)
		}
	} else {
		var err error
		session, err = start(launch.Rows, launch.Columns)
		if err != nil {
			return 1, err
		}
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
			_ = handler.Shutdown(drain)
			_ = server.Close()
		}
	}()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return 1, fmt.Errorf("generate worker secret: %w", err)
	}
	record := WorkerRecord{Session: launch.Session, ID: filepath.Base(directory), PID: os.Getpid(), Address: listener.Addr().String(), Secret: hex.EncodeToString(secret[:])}
	if launch.Transport == "acp" {
		record.Session.Transport = "acp"
		record.Session.AgentSessionID = launch.Session.AgentSessionID
		if conversation != nil {
			record.Session.ConversationState = conversation.ConversationState()
		}
	}
	if session != nil {
		record.Session.State = session.State()
	}
	if err := upsertSession(ctx, store.DB, record.Session); err != nil {
		return 1, fmt.Errorf("register session: %w", err)
	}
	endpoint := record
	endpoint.Session = SessionInfo{}
	data, err := json.Marshal(endpoint)
	if err != nil {
		return 1, err
	}
	// Publish a complete record; node discovery ignores temporary files.
	temporary := filepath.Join(directory, "worker.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return 1, fmt.Errorf("write worker record: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(directory, "worker.json")); err != nil {
		return 1, fmt.Errorf("publish worker record: %w", err)
	}
	var stateWriteWarning sync.Once
	persistConversationState := func(state string) {
		if err := patchWorkerConversationState(context.Background(), store.DB, record.ID, state); err != nil {
			stateWriteWarning.Do(func() {
				_, event := obs.Start(ctx, "worker", "persist_state")
				event.Set("session_id", record.ID)
				event.Fail(err)
				event.End()
				fmt.Fprintln(os.Stderr, "warning: ACP worker state could not be persisted")
			})
		}
	}
	if conversation != nil {
		conversation.SetStateChange(record.Session.ConversationState, persistConversationState)
	}
	handler, err = WorkerHandler(ctx, session, conversation, record.Secret, directory)
	if err != nil {
		return 1, err
	}
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
			next, cleanup, err := startACPConversation(obs.WithRecording(ctx, handler.diagnostics), launch, directory)
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
			if err := upsertSession(ctx, store.DB, launch.Session); err != nil {
				if cleanup != nil {
					cleanup()
				}
				return err
			}
			conversation = next
			acpCleanup = cleanup
			handler.mu.Lock()
			handler.conversation = next
			handler.mu.Unlock()
			next.SetStateChange(next.ConversationState(), persistConversationState)
			return nil
		}
	}
	server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	serveDone := make(chan error, 1)
	obs.Go("worker.http", func() { serveDone <- server.Serve(listener) })
	if err := ready(record.ID); err != nil {
		return 1, fmt.Errorf("announce worker: %w", err)
	}
	started.End()
	select {
	case <-ctx.Done():
		return 1, ctx.Err()
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
