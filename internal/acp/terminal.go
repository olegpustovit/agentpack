package acp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"

	sdkacp "github.com/coder/acp-go-sdk"
)

const (
	defaultTerminalOutputLimit = 256 << 10
	maxTerminalOutputLimit     = 4 << 20
)

var errUnknownTerminal = errors.New("unknown terminal id")

// TerminalPool runs ACP terminal/create commands with a bounded output buffer.
type TerminalPool struct {
	mu            sync.Mutex
	next          uint64
	live          map[string]*managedTerminal
	closed        bool
	startTerminal func(*exec.Cmd) (*managedTerminal, error)
}

type managedTerminal struct {
	mu           sync.Mutex
	limit        int
	output       []byte
	done         chan struct{}
	cmd          *exec.Cmd
	readOut      func([]byte) (int, error)
	closeOut     func() error
	waitCmd      func() error
	killCmd      func() error
	forceKillCmd func() error
	finishExit   func(error)
	exitCode     *int
	signal       *string
	released     bool
}

// NewTerminalPool returns an empty terminal pool.
func NewTerminalPool() *TerminalPool {
	return &TerminalPool{live: make(map[string]*managedTerminal), startTerminal: startManagedTerminal}
}

func terminalOutputLimit(request *int) int {
	if request == nil || *request <= 0 {
		return defaultTerminalOutputLimit
	}
	if *request >= maxTerminalOutputLimit {
		return maxTerminalOutputLimit
	}
	return *request
}

func (p *TerminalPool) create(root string, extra []string, req sdkacp.CreateTerminalRequest) (string, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return "", errors.New("terminal pool is closed")
	}
	limit := terminalOutputLimit(req.OutputByteLimit)
	cwd := root
	if req.Cwd != nil && *req.Cwd != "" {
		resolved, err := ResolveWorkspacePath(root, extra, *req.Cwd)
		if err != nil {
			return "", err
		}
		cwd = resolved
	}
	command := exec.Command(req.Command, req.Args...)
	command.Dir = cwd
	command.Env = terminalEnv(req.Env)
	term, err := p.startTerminal(command)
	if err != nil {
		return "", err
	}
	term.limit = limit
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = term.kill()
		if term.forceKillCmd != nil {
			_ = term.forceKillCmd()
		}
		if term.closeOut != nil {
			_ = term.closeOut()
		}
		term.waitAndRecord()
		return "", errors.New("terminal pool is closed")
	}
	p.next++
	id := strconv.FormatUint(p.next, 10)
	p.live[id] = term
	p.mu.Unlock()
	go term.run()
	return id, nil
}

func (p *TerminalPool) get(id string) (*managedTerminal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	term, ok := p.live[id]
	if !ok || term.released {
		return nil, errUnknownTerminal
	}
	return term, nil
}

func (p *TerminalPool) release(ctx context.Context, id string) error {
	p.mu.Lock()
	term, ok := p.live[id]
	if !ok || term.released {
		p.mu.Unlock()
		return errUnknownTerminal
	}
	term.released = true
	p.mu.Unlock()
	var errs []error
	if err := term.kill(); err != nil {
		errs = append(errs, err)
	}
	if term.forceKillCmd != nil {
		if err := term.forceKillCmd(); err != nil {
			errs = append(errs, err)
		}
	}
	select {
	case <-term.done:
	case <-ctx.Done():
		return errors.Join(append(errs, ctx.Err())...)
	}
	p.mu.Lock()
	if p.live[id] == term {
		delete(p.live, id)
	}
	p.mu.Unlock()
	return errors.Join(errs...)
}

// Close prevents new terminals and terminates every managed process group.
func (p *TerminalPool) Close() error {
	p.mu.Lock()
	p.closed = true
	terminals := make([]*managedTerminal, 0, len(p.live))
	for id, term := range p.live {
		delete(p.live, id)
		term.released = true
		terminals = append(terminals, term)
	}
	p.mu.Unlock()
	var errs []error
	for _, term := range terminals {
		if err := term.kill(); err != nil {
			errs = append(errs, err)
		}
		if term.forceKillCmd != nil {
			if err := term.forceKillCmd(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (p *TerminalPool) output(id string) (sdkacp.TerminalOutputResponse, error) {
	term, err := p.get(id)
	if err != nil {
		return sdkacp.TerminalOutputResponse{}, err
	}
	return term.snapshot(), nil
}

func (p *TerminalPool) wait(ctx context.Context, id string) (sdkacp.WaitForTerminalExitResponse, error) {
	term, err := p.get(id)
	if err != nil {
		return sdkacp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-ctx.Done():
		return sdkacp.WaitForTerminalExitResponse{}, ctx.Err()
	case <-term.done:
	}
	code, signal := term.exitValues()
	return sdkacp.WaitForTerminalExitResponse{ExitCode: code, Signal: signal}, nil
}

func (p *TerminalPool) kill(id string) error {
	term, err := p.get(id)
	if err != nil {
		return err
	}
	return term.kill()
}

func terminalEnv(vars []sdkacp.EnvVariable) []string {
	extra := make(map[string]string, len(vars))
	for _, item := range vars {
		if !isAgentpackSessionEnv(item.Name) {
			extra[item.Name] = item.Value
		}
	}
	out := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, line := range os.Environ() {
		name, _, ok := cutEnvLine(line)
		if !ok || isAgentpackSessionEnv(name) {
			continue
		}
		if _, set := extra[name]; set {
			continue
		}
		out = append(out, line)
	}
	for name, value := range extra {
		out = append(out, name+"="+value)
	}
	return append(out, "AGENTPACK_SESSION=1")
}

func isAgentpackSessionEnv(name string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, "AGENTPACK_SESSION")
	}
	return name == "AGENTPACK_SESSION"
}

func cutEnvLine(line string) (string, string, bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == '=' {
			return line[:i], line[i+1:], true
		}
	}
	return line, "", false
}

func (t *managedTerminal) appendOutput(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, chunk...)
	if extra := len(t.output) - t.limit; extra > 0 {
		t.output = append([]byte(nil), t.output[extra:]...)
	}
}

func (t *managedTerminal) snapshot() sdkacp.TerminalOutputResponse {
	t.mu.Lock()
	raw := append([]byte(nil), t.output...)
	t.mu.Unlock()
	text := string(raw)
	if text == "" {
		text = " "
	}
	truncated := len(raw) >= t.limit
	select {
	case <-t.done:
		code, signal := t.exitValues()
		if code == nil && signal == nil {
			return sdkacp.TerminalOutputResponse{Output: text, Truncated: truncated}
		}
		return sdkacp.TerminalOutputResponse{
			Output:     text,
			Truncated:  truncated,
			ExitStatus: &sdkacp.TerminalExitStatus{ExitCode: code, Signal: signal},
		}
	default:
		return sdkacp.TerminalOutputResponse{Output: text, Truncated: truncated}
	}
}

func (t *managedTerminal) exitValues() (*int, *string) {
	return t.exitCode, t.signal
}

func (t *managedTerminal) kill() error {
	if t.killCmd == nil {
		return nil
	}
	return t.killCmd()
}

func (t *managedTerminal) run() {
	t.collectOutput()
	t.waitAndRecord()
}

func (t *managedTerminal) waitAndRecord() {
	var err error
	if t.waitCmd != nil {
		err = t.waitCmd()
	}
	if t.finishExit != nil {
		t.finishExit(err)
	} else {
		recordProcessExit(t, err)
	}
	close(t.done)
}

func recordProcessExit(t *managedTerminal, err error) {
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			code = exit.ExitCode()
		}
	}
	t.exitCode = &code
	t.signal = nil
}

func (t *managedTerminal) collectOutput() {
	if t.readOut == nil {
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := t.readOut(buf)
		if n > 0 {
			t.appendOutput(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) || terminalReadDone(err) {
				return
			}
			return
		}
	}
}
