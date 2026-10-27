//go:build linux || darwin

package network

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type terminal struct{ *os.File }

func startTerminal(command *exec.Cmd, rows, columns uint16) (*terminal, error) {
	file, err := pty.StartWithSize(command, &pty.Winsize{Rows: rows, Cols: columns})
	if err != nil {
		return nil, err
	}
	// Darwin's PTY is opened outside Go's poller; register a nonblocking duplicate
	// so deadlines and Close interrupt reads on both Unix platforms.
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		name := file.Name()
		_ = file.Close()
		if err = unix.SetNonblock(fd, true); err == nil {
			file = os.NewFile(uintptr(fd), name)
			err = file.SetWriteDeadline(time.Time{})
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
	return &terminal{file}, nil
}

func resizeTerminal(terminal *terminal, rows, columns uint16) error {
	connection, err := terminal.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	// pty.Setsize calls File.Fd, which switches the descriptor back to blocking.
	err = connection.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: columns})
	})
	return errors.Join(err, ioctlErr)
}

func terminalEOF(err error) bool { return errors.Is(err, syscall.EIO) }

func (*terminal) wait(command *exec.Cmd) error { return command.Wait() }

func (terminal *terminal) finish(readDone <-chan struct{}) {
	// Descendants may retain the slave after the agent exits.
	_ = terminal.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	<-readDone
	_ = terminal.Close()
}

func (*terminal) terminate(process *os.Process, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// ConfigureDetached isolates workers from the launching terminal's lifetime.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
