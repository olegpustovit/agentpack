package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func (runner Runner) runProcess(command *exec.Cmd) (int, error) {
	// The worker signals the entire PTY group. Keep this wrapper alive long
	// enough to reap the harness and run its credential/overlay cleanup.
	if os.Getenv("AGENTPACK_SESSION_WORKER") == "1" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
	}
	err := command.Start()
	if err != nil {
		return 1, err
	}
	if runner.Launch != nil {
		cleanup, launchErr := runner.Launch(command.Process)
		if cleanup != nil {
			defer cleanup()
		}
		if launchErr != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return 1, launchErr
		}
	}
	err = command.Wait()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 1, err
}
