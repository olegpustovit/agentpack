package acp

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
)

func TestTerminalEnvForcesSessionMarker(t *testing.T) {
	t.Setenv("AGENTPACK_SESSION", "inherited-value")
	vars := []sdkacp.EnvVariable{{Name: "AGENTPACK_SESSION", Value: "request-value"}}
	if runtime.GOOS == "windows" {
		vars = append(vars, sdkacp.EnvVariable{Name: "agentpack_session", Value: "case-variant"})
	}
	env := terminalEnv(vars)
	markerCount := 0
	for _, line := range env {
		name, value, ok := cutEnvLine(line)
		if !ok || !isAgentpackSessionEnv(name) {
			continue
		}
		markerCount++
		if name != "AGENTPACK_SESSION" || value != "1" {
			t.Fatalf("session marker = %q, want AGENTPACK_SESSION=1", line)
		}
	}
	if markerCount != 1 {
		t.Fatalf("found %d session markers, want exactly one", markerCount)
	}
}

func TestTerminalOutputLimitIsBounded(t *testing.T) {
	large := maxTerminalOutputLimit * 2
	small := 1024
	for _, tc := range []struct {
		name string
		in   *int
		want int
	}{
		{name: "default", want: defaultTerminalOutputLimit},
		{name: "nonpositive", in: new(int), want: defaultTerminalOutputLimit},
		{name: "requested", in: &small, want: small},
		{name: "maximum", in: &large, want: maxTerminalOutputLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalOutputLimit(tc.in); got != tc.want {
				t.Fatalf("terminalOutputLimit(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
func TestTerminalPoolCloseTerminatesManagedProcesses(t *testing.T) {
	pool := NewTerminalPool()
	terminated, forced := 0, 0
	pool.live["1"] = &managedTerminal{
		killCmd:      func() error { terminated++; return nil },
		forceKillCmd: func() error { forced++; return nil },
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if terminated != 1 || forced != 1 || len(pool.live) != 0 {
		t.Fatalf("close counts = TERM %d KILL %d live %d", terminated, forced, len(pool.live))
	}
	if _, err := pool.create("", nil, sdkacp.CreateTerminalRequest{}); err == nil {
		t.Fatal("closed pool accepted a terminal")
	}
}

func TestTerminalPoolCloseReapsUnregisteredTerminal(t *testing.T) {
	pool := NewTerminalPool()
	started, resume := make(chan struct{}), make(chan struct{})
	alive, readerOpen, reaped := true, true, false
	var cleanup []string
	term := &managedTerminal{done: make(chan struct{})}
	term.killCmd = func() error {
		cleanup = append(cleanup, "term")
		return nil
	}
	term.forceKillCmd = func() error {
		cleanup = append(cleanup, "kill")
		alive = false
		return nil
	}
	term.closeOut = func() error {
		cleanup = append(cleanup, "close")
		readerOpen = false
		return nil
	}
	term.waitCmd = func() error {
		cleanup = append(cleanup, "wait")
		reaped = !alive && !readerOpen
		return nil
	}
	pool.startTerminal = func(*exec.Cmd) (*managedTerminal, error) {
		close(started)
		<-resume
		return term, nil
	}
	created := make(chan error, 1)
	go func() {
		_, err := pool.create("", nil, sdkacp.CreateTerminalRequest{Command: "fake"})
		created <- err
	}()
	<-started
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-created; err == nil {
		t.Fatal("closed pool accepted a terminal")
	}
	if alive || readerOpen || !reaped {
		t.Fatalf("cleanup left process alive=%t reader open=%t reaped=%t", alive, readerOpen, reaped)
	}
	if len(cleanup) != 4 || cleanup[0] != "term" || cleanup[1] != "kill" || cleanup[2] != "close" || cleanup[3] != "wait" {
		t.Fatalf("cleanup = %v, want [term kill close wait]", cleanup)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("rejected terminal completion was not settled")
	}
}
func TestTerminalPoolReleaseForceKillsTrackedProcess(t *testing.T) {
	pool := NewTerminalPool()
	done := make(chan struct{})
	terminated, forced, tracked := 0, 0, false
	term := &managedTerminal{done: done}
	term.killCmd = func() error {
		terminated++
		return nil
	}
	term.forceKillCmd = func() error {
		forced++
		pool.mu.Lock()
		tracked = pool.live["1"] == term && term.released
		pool.mu.Unlock()
		close(done)
		return nil
	}
	pool.live["1"] = term
	if err := pool.release(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if terminated != 1 || forced != 1 || !tracked || len(pool.live) != 0 {
		t.Fatalf("release TERM=%d KILL=%d tracked=%t live=%d", terminated, forced, tracked, len(pool.live))
	}
}
func TestTerminalPoolReleaseKillsStubbornProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("PTY terminals are unix-only in this build")
	}
	pool := NewTerminalPool()
	id, err := pool.create(t.TempDir(), nil, sdkacp.CreateTerminalRequest{
		Command: "sh", Args: []string{"-c", "trap '' TERM; sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	term, err := pool.get(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		if term.forceKillCmd != nil {
			_ = term.forceKillCmd()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.release(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.done:
	default:
		t.Fatal("released terminal process group is still running")
	}
}

func TestHarnessClientTerminal(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("PTY terminals are unix-only in this build")
	}
	root := t.TempDir()
	client := NewHarnessClient(root, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := client.CreateTerminal(ctx, sdkacp.CreateTerminalRequest{Command: "sh", Args: []string{"-c", "echo hello-acp"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.TerminalId == "" {
		t.Fatal("missing terminal id")
	}
	if _, err := client.WaitForTerminalExit(ctx, sdkacp.WaitForTerminalExitRequest{TerminalId: created.TerminalId}); err != nil {
		t.Fatal(err)
	}
	out, err := client.TerminalOutput(ctx, sdkacp.TerminalOutputRequest{TerminalId: created.TerminalId})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output == "" || out.Output == " " {
		t.Fatalf("output = %q", out.Output)
	}
	if _, err := client.ReleaseTerminal(ctx, sdkacp.ReleaseTerminalRequest{TerminalId: created.TerminalId}); err != nil {
		t.Fatal(err)
	}
}
