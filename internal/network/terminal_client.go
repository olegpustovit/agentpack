package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

// RunTerminal attaches directly to the worker, so gateway shutdown cannot
// interrupt the local terminal. Local and browser input share the same PTY.
// Closing the launching terminal stops its worker, including during reconnect.
func RunTerminal(ctx context.Context, home, id string, input *os.File, output io.Writer) (code int, result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ended := false
	defer func() {
		if ended || errors.Is(result, ErrSessionEnded) {
			return
		}
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		if err := StopWorker(stop, home, id); err != nil {
			code = 1
			result = errors.Join(result, fmt.Errorf("stop local terminal session: %w", err))
		}
	}()
	record, err := workerRecord(home, id)
	if err != nil {
		return 1, err
	}
	connection, err := attachWorker(ctx, record, true, 0)
	if err != nil {
		return 1, err
	}
	defer func() {
		if connection != nil {
			connection.Close()
		}
	}()
	consoleOutput, _ := output.(*os.File)
	restoreOutput, err := prepareConsoleOutput(consoleOutput)
	if err != nil {
		return 1, err
	}
	defer restoreOutput()
	saved, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return 1, err
	}
	defer term.Restore(int(input.Fd()), saved)
	connection.SetReadLimit(replayLimit + 4096)
	type terminalMessage struct {
		kind int
		data []byte
		err  error
	}
	incoming := make(chan terminalMessage, 1)
	done := make(chan struct{})
	defer close(done)
	read := func(connection *websocket.Conn, incoming chan terminalMessage) {
		for {
			kind, data, err := connection.ReadMessage()
			select {
			case incoming <- terminalMessage{kind, data, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}
	go read(connection, incoming)
	keys := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := input.Read(buffer)
			if n > 0 {
				data := append([]byte(nil), buffer[:n]...)
				select {
				case keys <- data:
				case <-done:
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()
	var attachment uint64
	var state TerminalState
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	write := func(kind int, data []byte) error {
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return connection.WriteMessage(kind, data)
	}
	// A stalled or dropped worker connection is transient while the agent runs:
	// reattach and replay instead of abandoning the agent. Unsent input is lost.
	reconnect := func() error {
		connection.Close()
		next, err := reattach(ctx, record)
		if err != nil {
			return err
		}
		connection = next
		connection.SetReadLimit(replayLimit + 4096)
		incoming = make(chan terminalMessage, 1)
		go read(connection, incoming)
		state = TerminalState{}
		attachment = 0
		_, err = fmt.Fprint(output, "\x1b[2J\x1b[H")
		return err
	}
	control := func(action string, rows, columns uint16) error {
		data, _ := json.Marshal(terminalControl{Action: action, Rows: rows, Columns: columns})
		return write(websocket.TextMessage, data)
	}
	for {
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case key := <-keys:
			if attachment != 0 && state.Running {
				if err := write(websocket.BinaryMessage, key); err != nil {
					if err := reconnect(); err != nil {
						return 1, err
					}
				}
			}
		case message := <-incoming:
			if message.err != nil {
				if err := reconnect(); err != nil {
					return 1, err
				}
				continue
			}
			if message.kind == websocket.BinaryMessage {
				if _, err := output.Write(message.data); err != nil {
					return 1, err
				}
				continue
			}
			var notice terminalNotice
			if err := json.Unmarshal(message.data, &notice); err != nil {
				return 1, err
			}
			state = notice.State
			if notice.Attachment != 0 {
				attachment = notice.Attachment
			}
			if notice.Type == "error" {
				// Never write transport notices over the application's terminal screen.
				if err := reconnect(); err != nil {
					return 1, err
				}
				continue
			}
			if notice.Type == "ended" {
				if state.Restarting || state.Running {
					if err := reconnect(); err != nil {
						return 1, err
					}
					continue
				}
				ended = true
				if notice.Error != "" {
					return 1, errors.New(notice.Error)
				}
				return state.ExitCode, nil
			}
		case <-ticker.C:
			columns, rows, err := ConsoleSize(input, consoleOutput)
			if err == nil && attachment != 0 && state.Running && (rows != int(state.Rows) || columns != int(state.Columns)) && validSize(uint16(rows), uint16(columns)) {
				if err := control("resize", uint16(rows), uint16(columns)); err != nil {
					if err := reconnect(); err != nil {
						return 1, err
					}
				}
			}
		}
	}
}

// reattach waits out restarts, a busy worker, and a not-yet-released previous
// attachment. It fails only when the session ended or the worker stopped
// answering at all (refused connections for several seconds), never on a timeout.
func reattach(ctx context.Context, record WorkerRecord) (*websocket.Conn, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		connection, err := tryReattach(ctx, record)
		if err == nil {
			return connection, nil
		}
		if errors.Is(err, ErrSessionEnded) {
			return nil, err
		}
		var netErr net.Error
		if errors.Is(err, errWorkerBusy) || errors.Is(err, websocket.ErrBadHandshake) || errors.As(err, &netErr) && netErr.Timeout() {
			failures = 0
			continue
		}
		if failures++; failures >= 50 {
			return nil, fmt.Errorf("agent session is no longer reachable: %w", err)
		}
	}
}

var errWorkerBusy = errors.New("agent is restarting")

func tryReattach(ctx context.Context, record WorkerRecord) (*websocket.Conn, error) {
	response, err := workerRequest(ctx, record, "GET", "/state", "")
	if err != nil {
		return nil, err
	}
	var state TerminalState
	err = json.NewDecoder(response.Body).Decode(&state)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	if !state.Running && !state.Restarting {
		return nil, ErrSessionEnded
	}
	if state.Restarting {
		return nil, errWorkerBusy
	}
	return attachWorker(ctx, record, true, 0)
}
