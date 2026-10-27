//go:build linux || darwin

package network

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

func TestTerminalChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_TERMINAL_CHILD") != "1" {
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		os.Exit(91)
	}
	fmt.Print("\x1b[2JREADY\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "size":
			columns, rows, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				os.Exit(92)
			}
			fmt.Printf("SIZE %d %d\n", rows, columns)
		case "exit":
			os.Exit(7)
		default:
			fmt.Printf("ECHO %s\n", scanner.Text())
		}
	}
	os.Exit(0)
}

func testSession(t *testing.T) *Session {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestTerminalChild$")
	command.Env = append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1")
	session, err := StartSession(command, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Stop(ctx)
		wait, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if _, err := session.Wait(wait); err != nil {
			t.Error(err)
		}
	})
	return session
}

func attachTest(t *testing.T, session *Session, local bool) (*Attachment, Replay) {
	t.Helper()
	attachment, replay, err := session.Attach(local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Detach(attachment) })
	return attachment, replay
}

func readUntil(t *testing.T, attachment *Attachment, prefix, want string) string {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
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

func TestSessionSharesPTYAndArbitratesInput(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	browser, replay := attachTest(t, session, false)
	if !bytes.Contains(replay.Data, []byte("\x1b[2JREADY")) {
		t.Fatalf("raw ANSI replay missing: %q", replay.Data)
	}
	if _, err := session.TakeControl(browser); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("browser stole control: %v", err)
	}
	if err := session.Resize(browser, session.State().Epoch, 30, 90); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("observer resized: %v", err)
	}
	old := session.State().Epoch
	if err := session.ReleaseControl(local, old); err != nil {
		t.Fatal(err)
	}
	state, err := session.TakeControl(browser)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.WriteInput(local, old, []byte("lost\n")); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("old owner typed: %v", err)
	}
	if err := session.Resize(browser, state.Epoch, 38, 111); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteInput(browser, state.Epoch, []byte("size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, local, "", "SIZE 38 111")
	readUntil(t, browser, "", "SIZE 38 111")
	if _, err := session.TakeControl(local); err != nil {
		t.Fatal(err)
	}
	if err := session.ReleaseControl(local, session.State().Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := session.TakeControl(browser); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteInput(browser, state.Epoch, []byte("stale\n")); !errors.Is(err, ErrStaleControl) {
		t.Fatalf("stale writer accepted: %v", err)
	}
	if err := session.WriteInput(browser, session.State().Epoch, []byte("héllo 世界\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, local, "", "ECHO héllo 世界")
	readUntil(t, browser, "", "ECHO héllo 世界")
	if err := session.WriteInput(browser, session.State().Epoch, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := session.Wait(ctx)
	if err != nil || code != 7 {
		t.Fatalf("exit = %d, %v", code, err)
	}
	if err := session.WriteInput(browser, session.State().Epoch, []byte("x")); !errors.Is(err, ErrSessionEnded) {
		t.Fatal(err)
	}
}

func TestDetachDoesNotStopSession(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	session.Detach(local)
	if !session.State().Running || session.State().Owner != 0 {
		t.Fatal(session.State())
	}
	browser, _ := attachTest(t, session, false)
	state, err := session.TakeControl(browser)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.WriteInput(browser, state.Epoch, []byte("still here\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, browser, "", "ECHO still here")
}

func TestSlowObserverAndBoundedReplay(t *testing.T) {
	session := &Session{state: TerminalState{Running: true}, attachments: make(map[uint64]*Attachment)}
	slow, _, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.TakeControl(slow); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), chunkSize)
	for range replayLimit/chunkSize + 2 {
		session.publish(chunk)
	}
	if !errors.Is(session.AttachmentError(slow), ErrSlowObserver) {
		t.Fatal("lagging observer not disconnected")
	}
	if session.State().Owner != 0 {
		t.Fatal("disconnected observer kept input")
	}
	session.Detach(slow) // Idempotent even after eviction.
	fresh, replay, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Data) != replayLimit || replay.Offset != 2*chunkSize {
		t.Fatalf("replay = %d at %d", len(replay.Data), replay.Offset)
	}
	replay.Data[0] = 'z'
	if session.replay[0] != 'x' {
		t.Fatal("replay exposes mutable session storage")
	}
	session.publish([]byte("next"))
	if got := string(<-fresh.Output); got != "next" {
		t.Fatal(got)
	}
}

func TestSessionValidatesBeforeSpawn(t *testing.T) {
	for _, size := range [][2]uint16{{0, 80}, {24, 0}, {1001, 80}, {24, 1001}} {
		command := exec.Command("does-not-exist")
		if _, err := StartSession(command, size[0], size[1]); err == nil || command.Process != nil {
			t.Fatal("invalid size started process")
		}
	}
	command := exec.Command("does-not-exist")
	command.Stdout = &bytes.Buffer{}
	if _, err := StartSession(command, 24, 80); err == nil {
		t.Fatal("accepted preassigned output")
	}
	if _, err := StartSession(exec.Command("does-not-exist"), 24, 80); err == nil {
		t.Fatal("spawn failure hidden")
	}
}
