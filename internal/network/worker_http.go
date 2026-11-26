package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

// Harness credential cleanup can wait up to a minute for its shared lock.
const workerStopGrace = 75 * time.Second

type WorkerHTTP struct {
	handler      http.Handler
	mu           sync.Mutex
	blobMu       sync.Mutex
	closing      bool
	connections  map[*websocket.Conn]struct{}
	drained      chan struct{}
	session      *Session
	Conversation *ConversationSession
	SessionDir   string
	StateStore   *state.Store
	// agentState is the last turn state a terminal agent reported (hooks, notify or its own output).
	agentState atomic.Value
	// agentSession is the agent's own session ID a terminal agent's hooks reported, for a native resume.
	agentSession atomic.Value
	// environment is what a chat's agent launched with, checked before a prompt; nil for terminals.
	environment *launchEnvironment
	// reloadFailed marks a chat whose last reload failed; its next prompt retries.
	reloadFailed atomic.Bool
	Start        func(uint16, uint16) (*Session, error)
	restartACP   func(context.Context) error
	stopACP      func(context.Context) error
	validate     func() error
	lifecycle    sync.Mutex
	done         chan struct{}
	Finished     bool
	stopping     bool
	Diagnostics  context.Context
	err          error
	attached     chan struct{}
	Restarts     map[string]error
	RestartIDs   []string
}

type RestartOperation struct {
	OperationID string `json:"operation_id" jsonschema:"Unique restart operation ID (16 to 128 letters, digits, hyphens or underscores). Reuse it when retrying the same restart; use a new ID for a new restart."`
}

func ValidOperationID(id string) bool {
	return len(id) >= 16 && len(id) <= 128 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) == -1
}

func (worker *WorkerHTTP) current() *Session {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.session
}

func (worker *WorkerHTTP) conversationSession() *ConversationSession {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.Conversation
}

func (worker *WorkerHTTP) firstAttachment() <-chan struct{} {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.attached
}

func (worker *WorkerHTTP) observe(ctx context.Context, name string) *obs.Event {
	_, event := obs.Start(obs.WithRecording(ctx, worker.Diagnostics), "worker", name)
	if worker.SessionDir != "" {
		event.Set("session_id", filepath.Base(worker.SessionDir))
	}
	if worker.current() != nil {
		// shortcut: PTYs merge chat and stderr, capture only when a backend exposes a separate diagnostic stream.
		event.Set("stderr_unavailable", "terminal streams are merged")
	}
	return event
}

func (worker *WorkerHTTP) observeStderr(event *obs.Event) {
	if conversation := worker.conversationSession(); conversation != nil && conversation.stderr != nil {
		event.Set("stderr", conversation.stderr.Redacted())
		if conversation.agentExitCode != nil {
			event.Set("exit_code", int(conversation.agentExitCode.Load()))
		}
	}
}

func (worker *WorkerHTTP) watch(session *Session) {
	<-session.Done
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
	if worker.current() == session && !worker.Finished {
		if !worker.stopping {
			code, err := session.Wait(context.Background())
			name := "exit"
			if code != 0 || err != nil {
				name = "unexpected_exit"
				if err == nil {
					err = fmt.Errorf("agent exited with status %d", code)
				}
			}
			event := worker.observe(worker.Diagnostics, name)
			event.Status = code
			event.Fail(err)
			event.End()
		}
		worker.Finished = true
		close(worker.done)
	}
}

// Restart keeps the private endpoint stable while replacing the agent process.
// Terminal attachments reconnect to the new PTY; ACP clients retain the worker endpoint.
func (worker *WorkerHTTP) Restart(ctx context.Context, operationID string) (result error) {
	if !ValidOperationID(operationID) {
		return errors.New("restart requires a valid operation_id")
	}
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
	if result, found := worker.Restarts[operationID]; found {
		return result
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	event := worker.observe(ctx, "restart")
	defer func() {
		event.Fail(result)
		if result != nil {
			event.Status = 1
		}
		worker.observeStderr(event)
		event.End()
	}()
	defer func() {
		if worker.Restarts == nil {
			worker.Restarts = make(map[string]error)
		}
		// shortcut: retain the last 128 operations per live worker; durable retry
		// history is needed only if workers gain recovery across process restarts.
		if len(worker.RestartIDs) == 128 {
			delete(worker.Restarts, worker.RestartIDs[0])
			worker.RestartIDs = worker.RestartIDs[1:]
		}
		worker.RestartIDs = append(worker.RestartIDs, operationID)
		worker.Restarts[operationID] = result
	}()
	if worker.Finished || worker.stopping {
		return errors.New("session cannot restart")
	}
	if worker.validate != nil {
		if err := worker.validate(); err != nil {
			return err
		}
	}
	if worker.conversationSession() != nil {
		if worker.restartACP == nil {
			return errors.New("session cannot restart")
		}
		// A failed reload keeps the chat open with a notice, and its next prompt retries.
		err := worker.restartACP(ctx)
		worker.reloadFailed.Store(err != nil)
		return errors.Join(err, worker.setReloadPending(err != nil))
	}
	if worker.Start == nil {
		return errors.New("session cannot restart")
	}
	session := worker.current()
	if err := session.MarkRestarting(); err != nil {
		return err
	}
	state := session.State()
	stop, cancel := context.WithTimeout(ctx, workerStopGrace)
	err := session.Stop(stop)
	cancel()
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		// A forced Stop returns before reaping. Never overlap agent generations.
		reap, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err = session.Wait(reap)
		cancel()
	}
	if err == nil {
		var replacement *Session
		replacement, err = worker.Start(state.Rows, state.Columns)
		if err == nil {
			replacement.OnAgentState(worker.recordAgentState)
			worker.mu.Lock()
			worker.session = replacement
			worker.attached = make(chan struct{})
			worker.mu.Unlock()
			obs.Go("worker.watch", func() { worker.watch(replacement) })
			return worker.setReloadPending(false)
		}
	}
	session.mu.Lock()
	session.Status.Restarting = false
	session.mu.Unlock()
	worker.err = err
	if !session.State().Running {
		worker.Finished = true
		close(worker.done)
	}
	return err
}

func (worker *WorkerHTTP) Stop(ctx context.Context) (result error) {
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
	if worker.Finished {
		return nil
	}
	var event *obs.Event
	if !worker.stopping {
		worker.stopping = true
		event = worker.observe(ctx, "stop")
	}
	defer func() {
		if event != nil {
			event.Fail(result)
			if session := worker.current(); session != nil {
				event.Status = session.State().ExitCode
			} else if conversation := worker.conversationSession(); conversation != nil && conversation.agentExitCode != nil {
				event.Status = int(conversation.agentExitCode.Load())
			}
			worker.observeStderr(event)
			event.End()
		}
		if result != nil && !errors.Is(result, context.Canceled) && !errors.Is(result, context.DeadlineExceeded) {
			worker.stopping = false
		}
	}()
	if worker.conversationSession() != nil {
		if worker.stopACP == nil {
			return errors.New("session cannot stop")
		}
		if err := worker.stopACP(ctx); err != nil {
			return err
		}
		worker.Finished = true
		close(worker.done)
		return nil
	}
	stop, cancel := context.WithTimeout(ctx, workerStopGrace)
	defer cancel()
	return worker.current().Stop(stop)
}

func (worker *WorkerHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	worker.handler.ServeHTTP(w, r)
}

// Shutdown drains final frames without signaling the child. net/http does not
// manage hijacked WebSocket connections, so the worker tracks them explicitly.
func (worker *WorkerHTTP) Shutdown(ctx context.Context) error {
	worker.mu.Lock()
	if !worker.closing {
		worker.closing = true
		if len(worker.connections) == 0 {
			close(worker.drained)
		}
	}
	worker.mu.Unlock()
	select {
	case <-worker.drained:
		return nil
	case <-ctx.Done():
		worker.mu.Lock()
		for connection := range worker.connections {
			_ = connection.Close()
		}
		worker.mu.Unlock()
		return ctx.Err()
	}
}

// WorkerHandler is private IPC. The gateway authenticates its own callers,
// then attaches using this worker's secret. Never expose the secret to browsers.
func WorkerHandler(ctx context.Context, pty *Session, conversation *ConversationSession, secret, sessionDir string) (*WorkerHTTP, error) {
	if (pty == nil) == (conversation == nil) {
		return nil, errors.New("worker requires exactly one of session or conversation")
	}
	if len(secret) < 32 {
		return nil, errors.New("worker requires a secret of at least 32 bytes")
	}
	worker := &WorkerHTTP{Diagnostics: ctx, session: pty, Conversation: conversation, SessionDir: sessionDir, connections: make(map[*websocket.Conn]struct{}), drained: make(chan struct{}), done: make(chan struct{}), attached: make(chan struct{})}
	if pty != nil {
		pty.OnAgentState(worker.recordAgentState)
	}
	if pty != nil {
		obs.Go("worker.watch", func() { worker.watch(pty) })
	}
	mux := http.NewServeMux()
	if pty != nil {
		worker.registerMCP(mux)
	}
	if conversation != nil {
		worker.RegisterConversation(mux)
	}
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		operationID := r.Header.Get("Idempotency-Key")
		if !ValidOperationID(operationID) {
			http.Error(w, "restart requires a valid Idempotency-Key", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
		defer cancel()
		// A change must not cut off a turn, a pending approval or a terminal agent's turn: reload once idle.
		if r.URL.Query().Get("when") == "idle" {
			deferred, err := worker.reloadWhenIdle(ctx, operationID)
			if err != nil {
				http.Error(w, "could not restart session", http.StatusConflict)
				return
			}
			if deferred {
				WriteJSON(w, map[string]bool{"deferred": true})
				return
			}
			WriteJSON(w, map[string]bool{"restarted": true})
			return
		}
		if err := worker.Restart(ctx, operationID); err != nil {
			http.Error(w, "could not restart session", http.StatusConflict)
			return
		}
		WriteJSON(w, map[string]bool{"restarted": true})
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "could not stop session", 500)
			return
		}
		WriteJSON(w, map[string]bool{"stopped": true})
	})
	// A terminal agent's hooks report its turns here (ReportAgentState); chats have their own state.
	mux.HandleFunc("POST /agent-state", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State          string `json:"state"`
			AgentSessionID string `json:"agent_session_id,omitempty"`
		}
		if pty == nil || worker.StateStore == nil || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil ||
			(body.State != string(StateWorking) && body.State != string(StateIdle) && body.State != string(StateNeedsYou)) {
			http.Error(w, "invalid agent state", http.StatusBadRequest)
			return
		}
		worker.agentState.Store(body.State)
		if body.AgentSessionID != "" {
			worker.agentSession.Store(body.AgentSessionID)
		}
		if body.State == string(StateIdle) && worker.reloadPending() {
			worker.reloadLater()
		}
		if err := PatchWorkerConversationState(r.Context(), worker.StateStore.DB, filepath.Base(worker.SessionDir), body.State); err != nil && !errors.Is(err, session.ErrSessionArchived) {
			_, event := obs.Start(r.Context(), "worker", "agent_state")
			event.Fail(err)
			event.End()
			http.Error(w, "could not record agent state", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if conversation := worker.conversationSession(); conversation != nil {
			// A pending reload shows as restarting; a failed one keeps the chat open for the retry.
			pending := worker.reloadPending()
			_ = json.NewEncoder(w).Encode(session.TerminalState{Running: !conversation.AgentExited() || pending, Restarting: pending && !worker.reloadFailed.Load(), ExitCode: -1})
			return
		}
		_ = json.NewEncoder(w).Encode(worker.current().State())
	})
	mux.HandleFunc("GET /attach", func(w http.ResponseWriter, r *http.Request) {
		if worker.current() == nil {
			http.Error(w, "session has no terminal attachment", http.StatusConflict)
			return
		}
		worker.attach(worker.current(), w, r)
	})
	worker.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Browser access belongs at the authenticated gateway, never this IPC.
		if r.Header.Get("Origin") != "" {
			http.Error(w, "worker does not accept browser origins", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			http.Error(w, "worker authentication required", http.StatusUnauthorized)
			return
		}
		ctx := obs.WithRecording(r.Context(), worker.Diagnostics)
		ctx = obs.WithTrace(ctx, r.Header.Get("traceparent"))
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return worker, nil
}

type TerminalControl struct {
	Action  string `json:"action"`
	Rows    uint16 `json:"rows,omitempty"`
	Columns uint16 `json:"columns,omitempty"`
}

type TerminalNotice struct {
	Type       string                `json:"type"`
	Attachment uint64                `json:"attachment,omitempty"`
	State      session.TerminalState `json:"state"`
	Offset     uint64                `json:"offset,omitempty"`
	Error      string                `json:"error,omitempty"`
}

func (worker *WorkerHTTP) attach(session *Session, w http.ResponseWriter, r *http.Request) {
	// A missing or malformed resume point replays the whole buffer.
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	attachment, replay, err := session.Attach(r.Header.Get("X-Agentpack-Local-Terminal") == "1", since)
	if err != nil {
		http.Error(w, "terminal cannot accept another attachment", http.StatusConflict)
		return
	}
	defer session.Detach(attachment)
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	worker.mu.Lock()
	if worker.closing {
		worker.mu.Unlock()
		return
	}
	worker.connections[connection] = struct{}{}
	if worker.session == session {
		select {
		case <-worker.attached:
		default:
			close(worker.attached)
		}
	}
	worker.mu.Unlock()
	defer func() {
		worker.mu.Lock()
		defer worker.mu.Unlock()
		delete(worker.connections, connection)
		if worker.closing && len(worker.connections) == 0 {
			close(worker.drained)
		}
	}()
	connection.SetReadLimit(ChunkSize)
	_ = connection.SetReadDeadline(time.Now().Add(45 * time.Second))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	write := func(kind int, payload []byte) error {
		if err := connection.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		return connection.WriteMessage(kind, payload)
	}
	notice := func(value TerminalNotice) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return write(websocket.TextMessage, data)
	}
	state := session.State()
	if notice(TerminalNotice{Type: "attached", Attachment: attachment.ID, State: state, Offset: replay.Offset}) != nil {
		return
	}
	if len(replay.Data) > 0 && write(websocket.BinaryMessage, replay.Data) != nil {
		return
	}
	if notice(TerminalNotice{Type: "replay_complete", Attachment: attachment.ID, State: state, Offset: replay.Offset + uint64(len(replay.Data))}) != nil {
		return
	}
	notices := make(chan TerminalNotice, 8)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			kind, data, err := connection.ReadMessage()
			if err != nil {
				return
			}
			switch kind {
			case websocket.BinaryMessage:
				err = session.WriteInput(attachment, data)
			case websocket.TextMessage:
				var control TerminalControl
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if err = decoder.Decode(&control); err != nil {
					return
				}
				if decoder.Decode(new(any)) != io.EOF {
					return
				}
				switch control.Action {
				case "resize":
					err = session.Resize(attachment, control.Rows, control.Columns)
				default:
					return
				}
			default:
				return
			}
			if err != nil {
				// Backend I/O errors may contain private paths. Publish only stable errors.
				message := "terminal operation failed"
				for _, safe := range []error{ErrLocalResize, ErrSessionEnded} {
					if errors.Is(err, safe) {
						message = safe.Error()
					}
				}
				select {
				case notices <- TerminalNotice{Type: "error", Error: message}:
				default:
					return
				}
			} else if kind == websocket.TextMessage {
				select {
				case notices <- TerminalNotice{Type: "state"}:
				default:
					return
				}
			}
		}
	}()
	stateTick := time.NewTicker(100 * time.Millisecond)
	defer stateTick.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-attachment.Ready:
			data, ok := attachment.ReadOutput()
			if !ok {
				message := ""
				if err := session.AttachmentError(attachment); err != nil {
					message = err.Error()
				}
				_ = notice(TerminalNotice{Type: "ended", State: session.State(), Error: message})
				return
			}
			if len(data) > 0 && write(websocket.BinaryMessage, data) != nil {
				return
			}
		case message := <-notices:
			message.State = session.State()
			state = message.State
			if notice(message) != nil {
				return
			}
		case <-stateTick.C:
			current := session.State()
			if current != state {
				state = current
				if notice(TerminalNotice{Type: "state", State: state}) != nil {
					return
				}
			}
		case <-ping.C:
			if write(websocket.PingMessage, nil) != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-closed:
			return
		}
	}
}

// recordAgentState writes a turn state a terminal agent announced in its own output. Repeats never
// touch the database.
func (worker *WorkerHTTP) recordAgentState(state string) {
	if worker.StateStore == nil || worker.agentState.Swap(state) == state {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := PatchWorkerConversationState(ctx, worker.StateStore.DB, filepath.Base(worker.SessionDir), state); err != nil && !errors.Is(err, session.ErrSessionArchived) {
		_, event := obs.Start(obs.WithRecording(ctx, worker.Diagnostics), "worker", "agent_state")
		event.Fail(err)
		event.End()
	}
}

func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func ReadJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	return ReadJSONLimit(w, r, value, 1<<20)
}

// ReadJSONLimit is ReadJSON for a route whose body may be larger than 1 MiB.
func ReadJSONLimit(w http.ResponseWriter, r *http.Request, value any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "expected one JSON object", 400)
		return false
	}
	return true
}

func WriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
