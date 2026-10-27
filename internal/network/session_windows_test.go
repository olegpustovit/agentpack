//go:build windows

package network

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func powershellCommand(name, script string) *exec.Cmd {
	code := utf16.Encode([]rune(script))
	encoded := make([]byte, len(code)*2)
	for i, unit := range code {
		binary.LittleEndian.PutUint16(encoded[i*2:], unit)
	}
	return exec.Command(name, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
}

func windowsSession(t *testing.T, command *exec.Cmd) *Session {
	t.Helper()
	session, err := StartSession(command, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := session.Stop(ctx); err != nil {
			t.Error(err)
		}
		if _, err := session.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	return session
}

func windowsReadUntil(t *testing.T, attachment *Attachment, prefix, want string) string {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for !strings.Contains(prefix, want) {
		select {
		case data, ok := <-attachment.Output:
			if !ok {
				t.Fatalf("stream ended before %q: %q", want, prefix)
			}
			prefix += string(data)
		case <-timer.C:
			t.Fatalf("waiting for %q; got %q", want, prefix)
		}
	}
	return prefix
}

func TestPowerShellTerminal(t *testing.T) {
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				if shell == "pwsh.exe" {
					t.Skip("PowerShell 7 is not installed")
				}
				t.Fatal(err)
			}
			script := `
$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::WriteLine(([char]27).ToString() + '[2JREADY ' + $env:AGENTPACK_TEST_VALUE)
[Console]::WriteLine('FOLDER ' + [Environment]::CurrentDirectory)
while (($line = [Console]::ReadLine()) -ne $null) {
  if ($line -eq 'size') { [Console]::WriteLine('SIZE ' + [Console]::WindowHeight + ' ' + [Console]::WindowWidth) }
  elseif ($line -eq 'exit') { [Console]::WriteLine('FINAL FRAME'); exit 7 }
  else { [Console]::WriteLine('ECHO ' + $line) }
}`
			command := powershellCommand(path, script)
			command.Dir = filepath.Join(t.TempDir(), "folder with spaces 世界")
			if err := os.Mkdir(command.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			command.Env = append(os.Environ(), "AGENTPACK_TEST_VALUE=héllo")
			session := windowsSession(t, command)
			local, replay, err := session.Attach(true)
			if err != nil {
				t.Fatal(err)
			}
			initial := windowsReadUntil(t, local, string(replay.Data), "READY héllo")
			windowsReadUntil(t, local, initial, filepath.Base(command.Dir))
			browser, replay, err := session.Attach(false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(replay.Data, []byte("\x1b[")) {
				t.Fatalf("ANSI missing: %q", replay.Data)
			}
			if _, err := session.TakeControl(browser); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("browser stole local input: %v", err)
			}
			if err := session.ReleaseControl(local, session.State().Epoch); err != nil {
				t.Fatal(err)
			}
			owner, err := session.TakeControl(browser)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Resize(browser, owner.Epoch, 38, 111); err != nil {
				t.Fatal(err)
			}
			if err := session.WriteInput(browser, owner.Epoch, []byte("size\r")); err != nil {
				t.Fatal(err)
			}
			windowsReadUntil(t, browser, "", "SIZE 38 111")
			session.Detach(local)
			session.Detach(browser)
			if !session.State().Running {
				t.Fatal("disconnect killed PowerShell")
			}
			local, _, err = session.Attach(true)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.WriteInput(local, session.State().Epoch, []byte("héllo 世界\r")); err != nil {
				t.Fatal(err)
			}
			windowsReadUntil(t, local, "", "ECHO héllo 世界")
			if err := session.WriteInput(local, session.State().Epoch, []byte("exit\r")); err != nil {
				t.Fatal(err)
			}
			windowsReadUntil(t, local, "", "FINAL FRAME")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if code, err := session.Wait(ctx); err != nil || code != 7 {
				t.Fatalf("exit = %d, %v", code, err)
			}
		})
	}
}

func TestWindowsConsolePipeDeadline(t *testing.T) {
	master, slave, err := consolePipe()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer windows.CloseHandle(slave)
	if err := master.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write(bytes.Repeat([]byte("x"), chunkSize*4)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked console input did not expire: %v", err)
	}
}

func TestWindowsSessionStopKillsDescendants(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	command := powershellCommand("powershell.exe", `
$child = Start-Process powershell.exe -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep 120' -PassThru
[IO.File]::WriteAllText($env:AGENTPACK_TEST_PID_FILE, [string]$child.Id)
[Console]::WriteLine('CHILD READY')
Start-Sleep 120
`)
	command.Env = append(os.Environ(), "AGENTPACK_TEST_PID_FILE="+pidPath)
	session := windowsSession(t, command)
	observer, replay, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	windowsReadUntil(t, observer, string(replay.Data), "CHILD READY")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	var pid uint32
	if _, err := fmt.Sscan(string(data), &pid); err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// PowerShell can ignore CTRL_BREAK while sleeping; the expired grace must
	// still terminate its entire job, including children with their own console.
	if err := session.Stop(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	reaped, cancelReap := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReap()
	if _, err := session.Wait(reaped); err != nil {
		t.Fatal(err)
	}
	if event, err := windows.WaitForSingleObject(child, 5000); err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived session stop: %d, %v", event, err)
	}
}

// These subprocesses exercise the real control-event path used by the CLI.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "session-signal":
			pid, err := strconv.ParseUint(os.Args[2], 10, 32)
			if err != nil || SignalConsole(uint32(pid)) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "session-test-output":
			var original, enabled, restored uint32
			handle := windows.Handle(os.Stdout.Fd())
			if windows.GetConsoleMode(handle, &original) != nil {
				os.Exit(1)
			}
			restore, err := prepareConsoleOutput(os.Stdout)
			if err != nil || windows.GetConsoleMode(handle, &enabled) != nil || enabled&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING == 0 {
				os.Exit(1)
			}
			columns, rows, err := ConsoleSize(os.Stdin, os.Stdout)
			restore()
			if err != nil || windows.GetConsoleMode(handle, &restored) != nil || restored != original {
				os.Exit(1)
			}
			fmt.Printf("CONSOLE %d %d\n", columns, rows)
			os.Exit(0)
		case "session-test-child":
			fmt.Println("CHILD READY")
			time.Sleep(time.Minute)
			os.Exit(0)
		case "session-test-wrapper":
			interrupts := make(chan os.Signal, 1)
			signal.Notify(interrupts, os.Interrupt)
			child := exec.Command(os.Args[0], "session-test-child")
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			_ = child.Run()
			// Simulate credential synchronization after the harness exits.
			if err := os.WriteFile(os.Args[2], []byte("saved"), 0600); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestWindowsSessionStopPreservesWrapperCleanup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "credentials-synchronized")
	session := windowsSession(t, exec.Command(os.Args[0], "session-test-wrapper", marker))
	observer, replay, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	windowsReadUntil(t, observer, string(replay.Data), "CHILD READY")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "saved" {
		t.Fatalf("wrapper cleanup skipped: %q %v", data, err)
	}
}

func TestWindowsConsoleOutputModeAndSize(t *testing.T) {
	session := windowsSession(t, exec.Command(os.Args[0], "session-test-output"))
	observer, replay, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	windowsReadUntil(t, observer, string(replay.Data), "CONSOLE 80 24")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if code, err := session.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("console mode helper: %d %v", code, err)
	}
}
