//go:build !windows

package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRunProcessForwardsSignalsAndCleansUp(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_SIGNAL_WRAPPER") == "" {
		for _, sig := range []string{"TERM", "HUP"} {
			t.Run(sig, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				wrapper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunProcessForwardsSignalsAndCleansUp$")
				wrapper.Env = append(os.Environ(), "AGENTPACK_TEST_SIGNAL_WRAPPER="+sig, "AGENTPACK_SESSION_WORKER=", "GORACE=atexit_sleep_ms=0")
				if output, err := wrapper.CombinedOutput(); err != nil {
					t.Fatalf("wrapper failed: %v\n%s", err, output)
				}
			})
		}
		return
	}
	kind := os.Getenv("AGENTPACK_TEST_SIGNAL_WRAPPER")
	sig := syscall.SIGTERM
	if kind == "HUP" {
		sig = syscall.SIGHUP
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("trap 'exit 42' %s; echo ready; while :; do :; done", kind))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	runner := Runner{Launch: func(*os.Process) (func(), error) {
		if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
			return nil, err
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return nil, err
		}
		return func() { cleaned = true }, syscall.Kill(os.Getpid(), sig)
	}}
	code, err := runner.runProcess(cmd)
	if err != nil || code != 42 || !cleaned {
		t.Fatalf("exit=%d err=%v cleaned=%t", code, err, cleaned)
	}
}
