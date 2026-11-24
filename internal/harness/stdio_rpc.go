package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// StdioRPC exchanges newline-delimited requests with a native probe command.
type StdioRPC struct {
	ctx                   context.Context
	cmd                   *exec.Cmd
	stdin                 io.WriteCloser
	lines                 *bufio.Scanner
	process, responseName string
}

// StartStdioRPC starts a probe command bound to ctx. The caller closes it after the last reply.
func StartStdioRPC(ctx context.Context, cmd *exec.Cmd, process, responseName string) (*StdioRPC, error) {
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	return &StdioRPC{ctx: ctx, cmd: cmd, stdin: stdin, lines: lines, process: process, responseName: responseName}, nil
}

func (rpc *StdioRPC) Close() {
	// Probe commands can stay alive after sending their reply.
	_ = rpc.stdin.Close()
	_ = rpc.cmd.Process.Kill()
	_ = rpc.cmd.Wait()
}

func (rpc *StdioRPC) Send(requests ...string) error {
	for _, request := range requests {
		if _, err := io.WriteString(rpc.stdin, request+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func (rpc *StdioRPC) Await(id string) (json.RawMessage, error) {
	for rpc.lines.Scan() {
		if rpc.ctx.Err() != nil {
			return nil, fmt.Errorf("%s timed out", rpc.process)
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rpc.lines.Bytes(), &message); err != nil || len(message.ID) == 0 || strings.Trim(string(message.ID), `"`) != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("%s: %s", rpc.responseName, message.Error.Message)
		}
		return message.Result, nil
	}
	if err := rpc.lines.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%s closed before responding", rpc.process)
}
