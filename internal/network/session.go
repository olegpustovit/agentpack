package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	replayLimit    = 8 << 20
	chunkSize      = 32 << 10
	observerLimit  = 1 << 20
	maxAttachments = 16
)

var (
	ErrLocalResize         = errors.New("only the local terminal may resize the shared grid")
	ErrSessionEnded        = errors.New("terminal session has ended")
	ErrSlowObserver        = errors.New("terminal observer fell behind; reconnect to recover available history")
	ErrUnsupportedTerminal = errors.New("terminal sharing is not supported on this platform")
)

// TerminalState contains no command arguments, environment, or credentials.
type TerminalState struct {
	Interactive  bool   `json:"interactive"`
	Running      bool   `json:"running"`
	Restarting   bool   `json:"restarting"`
	ShellCommand string `json:"shell_command,omitempty"`
	SharedInput  bool   `json:"shared_input,omitempty"`
	Responder    uint64 `json:"responder,omitempty"`
	Rows         uint16 `json:"rows"`
	Columns      uint16 `json:"columns"`
	ExitCode     int    `json:"exit_code"`
}

// Replay is a byte stream, not a terminal screen snapshot. Offset > 0 means
// earlier bytes were discarded, including potentially required ANSI state.
type Replay struct {
	Offset uint64
	Data   []byte
}

type Attachment struct {
	ID      uint64
	Ready   <-chan struct{}
	ready   chan struct{}
	pending []byte
	session *Session
	local   bool
	err     error
}

// ReadOutput drains queued bytes after Ready is signaled, preserving order across bursts.
func (attachment *Attachment) ReadOutput() ([]byte, bool) {
	session := attachment.session
	session.mu.Lock()
	defer session.mu.Unlock()
	data := attachment.pending
	attachment.pending = nil
	return data, len(data) > 0 || session.state.Running && session.attachments[attachment.ID] == attachment
}

// Session owns a single child and PTY. Gateway handlers only attach to it;
// closing an attachment never signals the child.
type Session struct {
	mu sync.Mutex
	// writeMu serializes input; it is never held with mu, so a child that stops reading input
	// cannot stall its output, attachments or state.
	writeMu     sync.Mutex
	command     *exec.Cmd
	terminal    *terminal
	state       TerminalState
	attachments map[uint64]*Attachment
	nextID      uint64
	replay      []byte
	offset      uint64
	done        chan struct{}
	readDone    chan struct{}
	readErr     error
	waitErr     error
	stopOnce    sync.Once
	stopErr     error
	shell       bool
	// agentState receives the turn states the agent announces in its own output (OSC 7501);
	// oscTail carries a report split across reads. Only readOutput touches oscTail.
	agentState func(string)
	oscTail    []byte
}

func StartSession(command *exec.Cmd, rows, columns uint16) (*Session, error) {
	if command == nil || command.Stdin != nil || command.Stdout != nil || command.Stderr != nil {
		return nil, errors.New("terminal command must have unassigned stdio")
	}
	if !validSize(rows, columns) {
		return nil, errors.New("terminal size must be between 1 and 1000 rows and columns")
	}
	terminal, err := startTerminal(command, rows, columns)
	if err != nil {
		return nil, fmt.Errorf("start terminal: %w", err)
	}
	session := &Session{
		command: command, terminal: terminal,
		state:       TerminalState{Interactive: true, Running: true, SharedInput: true, Rows: rows, Columns: columns, ExitCode: -1},
		attachments: make(map[uint64]*Attachment), done: make(chan struct{}), readDone: make(chan struct{}),
	}
	go session.readOutput()
	go session.reap()
	return session, nil
}

func validSize(rows, columns uint16) bool {
	return rows > 0 && columns > 0 && rows <= 1000 && columns <= 1000
}

func (session *Session) State() TerminalState {
	session.mu.Lock()
	defer session.mu.Unlock()
	state := session.state
	if session.shell && state.Running {
		state.ShellCommand = session.terminal.shellCommand(session.command.Process.Pid)
	}
	return state
}

// MarkRestarting lets attachments distinguish a supervised restart from an exit.
func (session *Session) MarkRestarting() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.state.Running {
		return ErrSessionEnded
	}
	session.state.Restarting = true
	return nil
}

// Attach atomically captures history and subscribes to all subsequent bytes.
// Only the private local transport may identify an attachment as local.
// Replay starts at since while that byte is still buffered; otherwise it is the whole buffer.
func (session *Session) Attach(local bool, since uint64) (*Attachment, Replay, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.attachments) >= maxAttachments {
		return nil, Replay{}, errors.New("terminal attachment limit reached")
	}
	if local {
		for _, attachment := range session.attachments {
			if attachment.local {
				return nil, Replay{}, errors.New("a local terminal is already attached")
			}
		}
	}
	session.nextID++
	output := make(chan struct{}, 1)
	attachment := &Attachment{ID: session.nextID, Ready: output, ready: output, session: session, local: local}
	session.attachments[attachment.ID] = attachment
	session.updateResponderLocked()
	if !session.state.Running {
		close(output)
	}
	replay := Replay{Offset: session.offset, Data: bytes.Clone(session.replay)}
	if end := session.offset + uint64(len(session.replay)); since >= session.offset && since <= end {
		replay = Replay{Offset: since, Data: bytes.Clone(session.replay[since-session.offset:])}
	}
	return attachment, replay, nil
}

func (session *Session) Detach(attachment *Attachment) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if current, ok := session.attachments[attachment.ID]; ok && current == attachment {
		delete(session.attachments, attachment.ID)
		if session.state.Running {
			close(attachment.ready)
		}
		session.updateResponderLocked()
	}
}

func (session *Session) AttachmentError(attachment *Attachment) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return attachment.err
}

func (session *Session) WriteInput(attachment *Attachment, data []byte) error {
	session.mu.Lock()
	err := session.attached(attachment)
	session.mu.Unlock()
	if err != nil {
		return err
	}
	return session.writeInput(data)
}

func (session *Session) writeInput(data []byte) error {
	if len(data) > chunkSize {
		return errors.New("terminal input exceeds 32 KiB")
	}
	session.mu.Lock()
	running := session.state.Running
	session.mu.Unlock()
	if !running {
		return ErrSessionEnded
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	// Keep a child that stops reading from blocking other input indefinitely.
	if err := session.terminal.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err := session.terminal.Write(data)
	return err
}

func (session *Session) Resize(attachment *Attachment, rows, columns uint16) error {
	if !validSize(rows, columns) {
		return errors.New("invalid terminal size")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.attached(attachment); err != nil {
		return err
	}
	if !attachment.local && !(session.shell && session.state.Responder == attachment.ID) {
		return ErrLocalResize
	}
	if err := resizeTerminal(session.terminal, rows, columns); err != nil {
		return err
	}
	session.state.Rows, session.state.Columns = rows, columns
	return nil
}

func (session *Session) attached(attachment *Attachment) error {
	if !session.state.Running {
		return ErrSessionEnded
	}
	if attachment == nil || session.attachments[attachment.ID] != attachment {
		return errors.New("terminal attachment is closed")
	}
	return nil
}

func (session *Session) updateResponderLocked() {
	var responder, local uint64
	for id, attachment := range session.attachments {
		if attachment.local {
			local = id
			break
		}
		if responder == 0 || id < responder {
			responder = id
		}
	}
	if local != 0 {
		responder = local
	}
	if !session.state.Running {
		responder = 0
	}
	session.state.Responder = responder
}

// publish reports whether a viewer is attached to answer terminal queries.
func (session *Session) publish(data []byte) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.replay = append(session.replay, data...)
	if extra := len(session.replay) - replayLimit; extra > 0 {
		session.replay = session.replay[extra:]
		session.offset += uint64(extra)
	}
	for id, attachment := range session.attachments {
		if len(attachment.pending)+len(data) > observerLimit {
			attachment.err = ErrSlowObserver
			close(attachment.ready)
			delete(session.attachments, id)
			session.updateResponderLocked()
			continue
		}
		attachment.pending = append(attachment.pending, data...)
		select {
		case attachment.ready <- struct{}{}:
		default:
		}
	}
	return session.state.Responder != 0
}

func (session *Session) readOutput() {
	defer close(session.readDone)
	buffer := make([]byte, chunkSize)
	for {
		n, err := session.terminal.Read(buffer)
		if n > 0 {
			data := bytes.Clone(buffer[:n])
			// Shells probe the terminal before any viewer attaches (fish waits 10 s), and a viewer never
			// answers replayed queries, so the worker answers Primary Device Attributes as xterm.js does.
			if !session.publish(data) && (bytes.Contains(data, []byte("\x1b[c")) || bytes.Contains(data, []byte("\x1b[0c"))) {
				_ = session.writeInput([]byte("\x1b[?1;2c"))
			}
			session.scanAgentState(data)
		}
		if err != nil {
			if !terminalEOF(err) && !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, os.ErrDeadlineExceeded) {
				session.readErr = fmt.Errorf("read terminal output: %w", err)
			}
			return
		}
	}
}

func (session *Session) reap() {
	err := session.terminal.wait(session.command)
	session.terminal.finish(session.readDone)
	session.mu.Lock()
	defer session.mu.Unlock()
	session.state.Running = false
	session.state.Responder = 0
	if session.command.ProcessState != nil {
		session.state.ExitCode = session.command.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		session.waitErr = err
	}
	session.waitErr = errors.Join(session.waitErr, session.readErr)
	for _, attachment := range session.attachments {
		close(attachment.ready)
	}
	close(session.done)
}

func (session *Session) Wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-session.done:
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.state.ExitCode, session.waitErr
	}
}

// Stop is explicit process termination. Gateway shutdown uses Detach instead.
func (session *Session) Stop(ctx context.Context) error {
	select {
	case <-session.done:
		return nil
	default:
	}
	session.stopOnce.Do(func() {
		session.stopErr = session.terminal.terminate(session.command.Process, false, session.shell)
		if errors.Is(session.stopErr, os.ErrProcessDone) {
			session.stopErr = nil
		}
	})
	if session.stopErr != nil {
		return session.stopErr
	}
	select {
	case <-session.done:
		return nil
	case <-ctx.Done():
		if err := session.terminal.terminate(session.command.Process, true, session.shell); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return ctx.Err()
	}
}

// OnAgentState makes the session report the turn states its agent announces in its own output.
func (session *Session) OnAgentState(report func(string)) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.agentState = report
}

// agentStateReport is how Codex's terminal UI announces its turns (0.169): ESC ] 7501 ; state=<state>[:app=codex]
// BEL, with working while a turn runs, done when it finishes, idle at the prompt and clear on exit.
var agentStateReport = []byte("\x1b]7501;state=")

func (session *Session) scanAgentState(data []byte) {
	session.mu.Lock()
	report := session.agentState
	session.mu.Unlock()
	if report == nil {
		return
	}
	text := append(session.oscTail, data...)
	session.oscTail = nil
	for {
		start := bytes.Index(text, agentStateReport)
		if start < 0 {
			// Keep just enough to find a report whose introducer is split across reads.
			keep := min(len(text), len(agentStateReport)-1)
			session.oscTail = bytes.Clone(text[len(text)-keep:])
			return
		}
		rest := text[start+len(agentStateReport):]
		end := bytes.IndexAny(rest, "\x07\x1b")
		if end < 0 {
			if len(rest) < 64 {
				session.oscTail = bytes.Clone(text[start:])
			}
			return
		}
		value, _, _ := strings.Cut(string(rest[:end]), ":")
		switch value {
		case "working":
			report(string(stateWorking))
		case "idle", "done":
			report(string(stateIdle))
		}
		text = rest[end:]
	}
}
