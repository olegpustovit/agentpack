package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func (runner Runner) runProcess(command *exec.Cmd) (int, error) {
	// Leave terminal interrupts to the child and stay alive for cleanup.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	err := command.Start()
	if err != nil {
		return 1, err
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case sig := <-signals:
				if sig != os.Interrupt {
					_ = command.Process.Signal(sig)
				}
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-stopped
	}()
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
