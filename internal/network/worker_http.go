package network

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Harness credential cleanup can wait up to a minute for its shared lock.
const workerStopGrace = 75 * time.Second

type WorkerHTTP struct {
	handler     http.Handler
	mu          sync.Mutex
	closing     bool
	connections map[*websocket.Conn]struct{}
	drained     chan struct{}
	session     *Session
	start       func(uint16, uint16) (*Session, error)
	validate    func() error
	lifecycle   sync.Mutex
	done        chan struct{}
	finished    bool
	err         error
	attached    chan struct{}
	restarts    map[string]error
	restartIDs  []string
}

type restartOperation struct {
	OperationID string `json:"operation_id" jsonschema:"Unique restart operation ID (16 to 128 letters, digits, hyphens or underscores). Reuse it when retrying the same restart; use a new ID for a new restart."`
}

func validRestartID(id string) bool {
	return len(id) >= 16 && len(id) <= 128 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) == -1
}

func (worker *WorkerHTTP) current() *Session {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.session
}

func (worker *WorkerHTTP) firstAttachment() <-chan struct{} {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.attached
}

func (worker *WorkerHTTP) watch(session *Session) {
	<-session.done
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
	if worker.current() == session && !worker.finished {
		worker.finished = true
		close(worker.done)
	}
}

// Restart keeps the private endpoint stable while the original launch pipeline
// rebuilds staging and starts a fresh agent. Old attachments receive an ended
// notice with Restarting=true and reconnect to the new PTY.
func (worker *WorkerHTTP) Restart(ctx context.Context, operationID string) (result error) {
	if !validRestartID(operationID) {
		return errors.New("restart requires a valid operation_id")
	}
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
	if result, found := worker.restarts[operationID]; found {
		return result
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if worker.restarts == nil {
			worker.restarts = make(map[string]error)
		}
		// shortcut: retain the last 128 operations per live worker; durable retry
		// history is needed only if workers gain recovery across process restarts.
		if len(worker.restartIDs) == 128 {
			delete(worker.restarts, worker.restartIDs[0])
			worker.restartIDs = worker.restartIDs[1:]
		}
		worker.restartIDs = append(worker.restartIDs, operationID)
		worker.restarts[operationID] = result
	}()
	if worker.finished || worker.start == nil {
		return errors.New("session cannot restart")
	}
	if worker.validate != nil {
		if err := worker.validate(); err != nil {
			return err
		}
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
		replacement, err = worker.start(state.Rows, state.Columns)
		if err == nil {
			worker.mu.Lock()
			worker.session = replacement
			worker.attached = make(chan struct{})
			worker.mu.Unlock()
			go worker.watch(replacement)
			return nil
		}
	}
	session.mu.Lock()
	session.state.Restarting = false
	session.mu.Unlock()
	worker.err = err
	if !session.State().Running {
		worker.finished = true
		close(worker.done)
	}
	return err
}

func (worker *WorkerHTTP) Stop(ctx context.Context) error {
	worker.lifecycle.Lock()
	defer worker.lifecycle.Unlock()
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
func WorkerHandler(session *Session, secret string) (*WorkerHTTP, error) {
	if session == nil || len(secret) < 32 {
		return nil, errors.New("worker requires a session and a secret of at least 32 bytes")
	}
	worker := &WorkerHTTP{session: session, connections: make(map[*websocket.Conn]struct{}), drained: make(chan struct{}), done: make(chan struct{}), attached: make(chan struct{})}
	go worker.watch(session)
	mux := http.NewServeMux()
	worker.registerMCP(mux)
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		operationID := r.Header.Get("Idempotency-Key")
		if !validRestartID(operationID) {
			http.Error(w, "restart requires a valid Idempotency-Key", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
		defer cancel()
		if err := worker.Restart(ctx, operationID); err != nil {
			http.Error(w, "could not restart session", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"restarted": true})
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, "could not stop session", 500)
			return
		}
		writeJSON(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(worker.current().State())
	})
	mux.HandleFunc("GET /attach", func(w http.ResponseWriter, r *http.Request) {
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
		mux.ServeHTTP(w, r)
	})
	return worker, nil
}

type terminalControl struct {
	Action  string `json:"action"`
	Rows    uint16 `json:"rows,omitempty"`
	Columns uint16 `json:"columns,omitempty"`
}

type terminalNotice struct {
	Type       string        `json:"type"`
	Attachment uint64        `json:"attachment,omitempty"`
	State      TerminalState `json:"state"`
	Offset     uint64        `json:"offset,omitempty"`
	Error      string        `json:"error,omitempty"`
}

func (worker *WorkerHTTP) attach(session *Session, w http.ResponseWriter, r *http.Request) {
	attachment, replay, err := session.Attach(r.Header.Get("X-Agentpack-Local-Terminal") == "1")
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
	connection.SetReadLimit(chunkSize)
	_ = connection.SetReadDeadline(time.Now().Add(45 * time.Second))
	connection.SetPongHandler(func(string) error { return connection.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	write := func(kind int, payload []byte) error {
		if err := connection.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		return connection.WriteMessage(kind, payload)
	}
	notice := func(value terminalNotice) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return write(websocket.TextMessage, data)
	}
	state := session.State()
	if notice(terminalNotice{Type: "attached", Attachment: attachment.ID, State: state, Offset: replay.Offset}) != nil {
		return
	}
	if len(replay.Data) > 0 && write(websocket.BinaryMessage, replay.Data) != nil {
		return
	}
	if notice(terminalNotice{Type: "replay_complete", Attachment: attachment.ID, State: state, Offset: replay.Offset + uint64(len(replay.Data))}) != nil {
		return
	}
	notices := make(chan terminalNotice, 8)
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
				var control terminalControl
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
				case notices <- terminalNotice{Type: "error", Error: message}:
				default:
					return
				}
			} else if kind == websocket.TextMessage {
				select {
				case notices <- terminalNotice{Type: "state"}:
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
				_ = notice(terminalNotice{Type: "ended", State: session.State(), Error: message})
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
				if notice(terminalNotice{Type: "state", State: state}) != nil {
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
