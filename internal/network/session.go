package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	replayLimit    = 8 << 20
	chunkSize      = 32 << 10
	observerQueue  = 32
	maxAttachments = 16
)

var (
	ErrNotOwner            = errors.New("terminal input belongs to another attachment")
	ErrStaleControl        = errors.New("terminal control has changed; refresh before sending input")
	ErrSessionEnded        = errors.New("terminal session has ended")
	ErrSlowObserver        = errors.New("terminal observer fell behind; reconnect to recover available history")
	ErrUnsupportedTerminal = errors.New("terminal sharing is not supported on this platform")
)

// TerminalState contains no command arguments, environment, or credentials.
type TerminalState struct {
	Interactive bool   `json:"interactive"`
	Running     bool   `json:"running"`
	Restarting  bool   `json:"restarting"`
	Owner       uint64 `json:"owner"`
	Epoch       uint64 `json:"epoch"`
	Rows        uint16 `json:"rows"`
	Columns     uint16 `json:"columns"`
	ExitCode    int    `json:"exit_code"`
}

// Replay is a byte stream, not a terminal screen snapshot. Offset > 0 means
// earlier bytes were discarded, including potentially required ANSI state.
type Replay struct {
	Offset uint64
	Data   []byte
}

type Attachment struct {
	ID     uint64
	Output <-chan []byte
	output chan []byte
	local  bool
	err    error
}

// Session owns a single child and PTY. Gateway handlers only attach to it;
// closing an attachment never signals the child.
type Session struct {
	mu          sync.Mutex
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
		state:       TerminalState{Interactive: true, Running: true, Rows: rows, Columns: columns, ExitCode: -1},
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
	return session.state
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
func (session *Session) Attach(local bool) (*Attachment, Replay, error) {
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
	output := make(chan []byte, observerQueue)
	attachment := &Attachment{ID: session.nextID, Output: output, output: output, local: local}
	session.attachments[attachment.ID] = attachment
	if local && session.state.Running {
		session.state.Owner = attachment.ID
		session.state.Epoch++
	}
	if !session.state.Running {
		close(output)
	}
	return attachment, Replay{Offset: session.offset, Data: bytes.Clone(session.replay)}, nil
}

func (session *Session) Detach(attachment *Attachment) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if current, ok := session.attachments[attachment.ID]; ok && current == attachment {
		delete(session.attachments, attachment.ID)
		if session.state.Running {
			close(attachment.output)
		}
		if session.state.Owner == attachment.ID {
			session.state.Owner = 0
			session.state.Epoch++
		}
	}
}

func (session *Session) AttachmentError(attachment *Attachment) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return attachment.err
}

// TakeControl never steals a writer lease. The local terminal may explicitly
// reclaim control; browser attachments must wait for the current owner to release.
func (session *Session) TakeControl(attachment *Attachment) (TerminalState, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.attached(attachment); err != nil {
		return session.state, err
	}
	if session.state.Owner != 0 && session.state.Owner != attachment.ID && !attachment.local {
		return session.state, ErrNotOwner
	}
	if session.state.Owner != attachment.ID {
		session.state.Owner = attachment.ID
		session.state.Epoch++
	}
	return session.state, nil
}

func (session *Session) ReleaseControl(attachment *Attachment, epoch uint64) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.owns(attachment, epoch); err != nil {
		return err
	}
	session.state.Owner = 0
	session.state.Epoch++
	return nil
}

func (session *Session) WriteInput(attachment *Attachment, epoch uint64, data []byte) error {
	if len(data) > chunkSize {
		return errors.New("terminal input exceeds 32 KiB")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.owns(attachment, epoch); err != nil {
		return err
	}
	// A child that stops reading must not hold the control lock indefinitely.
	if err := session.terminal.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err := session.terminal.Write(data)
	return err
}

func (session *Session) Resize(attachment *Attachment, epoch uint64, rows, columns uint16) error {
	if !validSize(rows, columns) {
		return errors.New("invalid terminal size")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if err := session.owns(attachment, epoch); err != nil {
		return err
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

func (session *Session) owns(attachment *Attachment, epoch uint64) error {
	if err := session.attached(attachment); err != nil {
		return err
	}
	if session.state.Owner != attachment.ID {
		return ErrNotOwner
	}
	if session.state.Epoch != epoch {
		return ErrStaleControl
	}
	return nil
}

func (session *Session) publish(data []byte) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.replay = append(session.replay, data...)
	if extra := len(session.replay) - replayLimit; extra > 0 {
		session.replay = session.replay[extra:]
		session.offset += uint64(extra)
	}
	for id, attachment := range session.attachments {
		select {
		case attachment.output <- data:
		default:
			attachment.err = ErrSlowObserver
			close(attachment.output)
			delete(session.attachments, id)
			if session.state.Owner == id {
				session.state.Owner = 0
				session.state.Epoch++
			}
		}
	}
}

func (session *Session) readOutput() {
	defer close(session.readDone)
	buffer := make([]byte, chunkSize)
	for {
		n, err := session.terminal.Read(buffer)
		if n > 0 {
			session.publish(bytes.Clone(buffer[:n]))
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
	session.state.Owner = 0
	session.state.Epoch++
	if session.command.ProcessState != nil {
		session.state.ExitCode = session.command.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		session.waitErr = err
	}
	session.waitErr = errors.Join(session.waitErr, session.readErr)
	for _, attachment := range session.attachments {
		close(attachment.output)
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
		session.stopErr = session.terminal.terminate(session.command.Process, false)
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
		if err := session.terminal.terminate(session.command.Process, true); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return ctx.Err()
	}
}
