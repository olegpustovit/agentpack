//go:build linux || darwin

package acp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func startManagedTerminal(command *exec.Cmd) (*managedTerminal, error) {
	if command == nil || command.Stdin != nil || command.Stdout != nil || command.Stderr != nil {
		return nil, errors.New("terminal command must have unassigned stdio")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	file, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		name := file.Name()
		_ = file.Close()
		if err = unix.SetNonblock(fd, true); err == nil {
			file = os.NewFile(uintptr(fd), name)
			_ = file.SetWriteDeadline(time.Time{})
		} else {
			_ = unix.Close(fd)
		}
	}
	if err != nil {
		_ = file.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	term := &managedTerminal{cmd: command, done: make(chan struct{})}
	term.readOut = file.Read
	term.closeOut = file.Close
	term.waitCmd = command.Wait
	term.finishExit = func(err error) { recordProcessExitUnix(term, err) }
	term.killCmd = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	term.forceKillCmd = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return term, nil
}

func recordProcessExitUnix(t *managedTerminal, err error) {
	code := -1
	var signal *string
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok {
				if status.Signaled() {
					name := status.Signal().String()
					signal = &name
				} else {
					c := status.ExitStatus()
					code = c
				}
			}
		}
	} else {
		code = 0
	}
	t.exitCode = &code
	t.signal = signal
}

func terminalReadDone(err error) bool {
	return errors.Is(err, syscall.EIO)
}
