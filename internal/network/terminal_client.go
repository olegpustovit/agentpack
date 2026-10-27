package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

// RunTerminal attaches directly to the worker, so gateway shutdown cannot
// interrupt the local terminal. Ctrl+] releases or reclaims input ownership.
func RunTerminal(ctx context.Context, home, id string, input *os.File, output io.Writer) (int, error) {
	record, err := workerRecord(home, id)
	if err != nil {
		return 1, err
	}
	connection, err := attachWorker(ctx, record, true)
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
				close(keys)
				return
			}
		}
	}()
	var attachment uint64
	var state TerminalState
	taking := false
	var pending []byte
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	write := func(kind int, data []byte) error {
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return connection.WriteMessage(kind, data)
	}
	control := func(action string, rows, columns uint16) error {
		data, _ := json.Marshal(terminalControl{Action: action, Epoch: state.Epoch, Rows: rows, Columns: columns})
		return write(websocket.TextMessage, data)
	}
	for {
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case key, ok := <-keys:
			if !ok {
				return 0, nil
			}
			for len(key) > 0 {
				index := -1
				for i, b := range key {
					if b == 29 {
						index = i
						break
					}
				}
				data := key
				if index >= 0 {
					data = key[:index]
				}
				if len(data) > 0 && taking {
					if len(pending)+len(data) > chunkSize {
						return 1, errors.New("input queued during control handoff exceeded 32 KiB")
					}
					pending = append(pending, data...)
				} else if len(data) > 0 && attachment != 0 && state.Owner == attachment {
					if err := write(websocket.BinaryMessage, data); err != nil {
						return 1, err
					}
				}
				if index < 0 {
					break
				}
				if taking {
					key = key[index+1:]
					continue
				}
				action := "take"
				if attachment != 0 && state.Owner == attachment {
					action = "release"
				}
				if err := control(action, 0, 0); err != nil {
					return 1, err
				}
				if action == "take" {
					taking = true
				} else {
					state.Owner = 0
				}
				key = key[index+1:]
			}
		case message := <-incoming:
			if message.err != nil {
				return 1, message.err
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
				if _, err := fmt.Fprintf(output, "\r\nagentpack: %s; pending input was not sent\r\n", notice.Error); err != nil {
					return 1, err
				}
				taking = false
				pending = nil
			} else if taking && attachment != 0 && state.Owner == attachment {
				taking = false
				if len(pending) > 0 {
					if err := write(websocket.BinaryMessage, pending); err != nil {
						return 1, err
					}
					pending = nil
				}
			}
			if notice.Type == "ended" {
				if state.Restarting {
					connection.Close()
					wait, cancel := context.WithTimeout(ctx, 90*time.Second)
					connection, err = reattachRestart(wait, record)
					cancel()
					if err != nil {
						return 1, err
					}
					connection.SetReadLimit(replayLimit + 4096)
					incoming = make(chan terminalMessage, 1)
					go read(connection, incoming)
					state = TerminalState{}
					attachment = 0
					taking = false
					pending = nil
					if _, err := fmt.Fprint(output, "\x1b[2J\x1b[Hagentpack: agent restarted; starting a fresh conversation\r\n"); err != nil {
						return 1, err
					}
					continue
				}
				if notice.Error != "" {
					return 1, errors.New(notice.Error)
				}
				return state.ExitCode, nil
			}
		case <-ticker.C:
			columns, rows, err := ConsoleSize(input, consoleOutput)
			if err == nil && state.Owner == attachment && (rows != int(state.Rows) || columns != int(state.Columns)) && validSize(uint16(rows), uint16(columns)) {
				if err := control("resize", uint16(rows), uint16(columns)); err != nil {
					return 1, err
				}
			}
		}
	}
}

func reattachRestart(ctx context.Context, record WorkerRecord) (*websocket.Conn, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
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
			if !state.Restarting {
				return attachWorker(ctx, record, true)
			}
		}
	}
}
