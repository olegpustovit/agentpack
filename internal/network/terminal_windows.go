//go:build windows

package network

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

type terminal struct {
	mu            sync.Mutex
	console       *conpty.ConPty
	input, output *os.File
	job           windows.Handle
}

func startTerminal(command *exec.Cmd, rows, columns uint16) (_ *terminal, retErr error) {
	input, inputSlave, err := consolePipe()
	if err != nil {
		return nil, err
	}
	output, outputSlave, err := consolePipe()
	if err != nil {
		_ = input.Close()
		_ = windows.CloseHandle(inputSlave)
		return nil, err
	}
	console, err := conpty.NewWithPipes(uintptr(inputSlave), 0, 0, uintptr(outputSlave), int(columns), int(rows), 0)
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = windows.CloseHandle(inputSlave)
		_ = windows.CloseHandle(outputSlave)
		return nil, fmt.Errorf("create ConPTY (Windows 10 1809 or later required): %w", err)
	}
	terminal := &terminal{console: console, input: input, output: output}
	defer func() {
		if retErr != nil {
			_ = terminal.input.Close()
			_ = terminal.output.Close()
			_ = terminal.console.Close()
			if terminal.job != 0 {
				_ = windows.CloseHandle(terminal.job)
			}
		}
	}()
	terminal.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(terminal.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	attrs := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attrs = *command.SysProcAttr
	}
	// Assign the job before any child code can spawn descendants outside it.
	attrs.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP
	pid, handle, err := console.Spawn(command.Path, command.Args, &syscall.ProcAttr{Dir: command.Dir, Env: command.Env, Sys: &attrs})
	if err != nil {
		return nil, err
	}
	process := windows.Handle(handle)
	defer windows.CloseHandle(process)
	defer func() {
		if retErr != nil {
			_ = windows.TerminateProcess(process, 1)
			_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
			if command.Process != nil {
				_ = command.Process.Release()
			}
		}
	}()
	if err := windows.AssignProcessToJobObject(terminal.job, process); err != nil {
		return nil, err
	}
	command.Process, err = os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	if err := resumeConsoleProcess(uint32(pid)); err != nil {
		return nil, err
	}
	return terminal, nil
}

// ConPTY requires synchronous slave handles. The master uses overlapped I/O so
// a child that stops reading cannot defeat the terminal input deadline.
func consolePipe() (*os.File, windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, 0, err
	}
	security, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, 0, err
	}
	name, err := windows.UTF16PtrFromString(`\\.\pipe\agentpack-terminal-` + rand.Text())
	if err != nil {
		return nil, 0, err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: security}
	master, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, chunkSize, chunkSize, 0, &attrs)
	if err != nil {
		return nil, 0, err
	}
	file := os.NewFile(uintptr(master), "agentpack terminal")
	slave, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err == nil {
		err = windows.ConnectNamedPipe(master, &windows.Overlapped{})
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			err = nil
		}
	}
	if err == nil {
		err = file.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = file.Close()
		if slave != windows.InvalidHandle {
			_ = windows.CloseHandle(slave)
		}
		return nil, 0, err
	}
	return file, slave, nil
}

func resumeConsoleProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("find suspended terminal thread: %w", err)
}

func (terminal *terminal) Read(data []byte) (int, error)  { return terminal.output.Read(data) }
func (terminal *terminal) Write(data []byte) (int, error) { return terminal.input.Write(data) }
func (terminal *terminal) SetWriteDeadline(deadline time.Time) error {
	return terminal.input.SetWriteDeadline(deadline)
}

func resizeTerminal(terminal *terminal, rows, columns uint16) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.console == nil {
		return os.ErrClosed
	}
	return terminal.console.Resize(int(columns), int(rows))
}

func terminalEOF(err error) bool { return errors.Is(err, windows.ERROR_BROKEN_PIPE) }

func (*terminal) wait(command *exec.Cmd) error {
	state, err := command.Process.Wait()
	command.ProcessState = state
	if err == nil && !state.Success() {
		err = &exec.ExitError{ProcessState: state}
	}
	return err
}

func (terminal *terminal) finish(readDone <-chan struct{}) {
	terminal.mu.Lock()
	console, job := terminal.console, terminal.job
	terminal.console = nil
	terminal.job = 0
	terminal.mu.Unlock()
	// ClosePseudoConsole emits a final frame; keep the reader running until it returns.
	_ = console.Close()
	_ = windows.CloseHandle(job)
	_ = terminal.output.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	<-readDone
	_ = terminal.input.Close()
	_ = terminal.output.Close()
}

func (terminal *terminal) terminate(process *os.Process, force bool) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.job == 0 {
		return os.ErrProcessDone
	}
	if !force {
		executable, err := os.Executable()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "session-signal", strconv.Itoa(process.Pid))
			ConfigureDetached(command)
			err = command.Run()
		}
		if err == nil {
			return nil
		}
		// Stop the job, but report that credential cleanup could not be requested.
		return errors.Join(fmt.Errorf("signal agent for cleanup: %w", err), windows.TerminateJobObject(terminal.job, 1))
	}
	return windows.TerminateJobObject(terminal.job, 1)
}

// SignalConsole runs only in the disposable session-signal process: attaching
// a console resets Windows control handlers, including Go's runtime handler.
func SignalConsole(pid uint32) error {
	if pid == 0 {
		return os.ErrInvalid
	}
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	attached, _, err := kernel.NewProc("AttachConsole").Call(uintptr(pid))
	if attached == 0 {
		return fmt.Errorf("attach session console: %w", err)
	}
	defer kernel.NewProc("FreeConsole").Call()
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pid)
}

// ConfigureDetached isolates workers from the launching PowerShell window.
func ConfigureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
