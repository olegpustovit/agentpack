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
		case <-attachment.Ready:
			data, ok := attachment.ReadOutput()
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

func TestSessionSharesPTYAndAllowsInputFromEveryAttachment(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	if state := session.State(); !state.SharedInput || state.Responder != local.ID {
		t.Fatalf("local should be the initial responder: %+v", state)
	}
	browser, replay := attachTest(t, session, false)
	if !bytes.Contains(replay.Data, []byte("\x1b[2JREADY")) {
		t.Fatalf("raw ANSI replay missing: %q", replay.Data)
	}
	if state := session.State(); state.Responder != local.ID {
		t.Fatalf("local did not retain responder priority: %+v", state)
	}
	if err := session.Resize(browser, 30, 90); !errors.Is(err, ErrLocalResize) {
		t.Fatalf("browser resized local grid: %v", err)
	}
	if err := session.Resize(local, 30, 90); err != nil {
		t.Fatalf("local could not resize host grid: %v", err)
	}
	if err := session.WriteInput(browser, []byte("size\n")); err != nil {
		t.Fatalf("browser input rejected: %v", err)
	}
	readUntil(t, local, "", "SIZE 30 90")
	readUntil(t, browser, "", "SIZE 30 90")
	if err := session.WriteInput(local, []byte("héllo 世界\n")); err != nil {
		t.Fatalf("local input rejected: %v", err)
	}
	readUntil(t, local, "", "ECHO héllo 世界")
	readUntil(t, browser, "", "ECHO héllo 世界")
	if err := session.WriteInput(browser, []byte("browser line\n")); err != nil {
		t.Fatalf("browser input rejected: %v", err)
	}
	readUntil(t, local, "", "ECHO browser line")
	readUntil(t, browser, "", "ECHO browser line")
	session.Detach(local)
	if state := session.State(); state.Responder != browser.ID {
		t.Fatalf("browser was not promoted when local detached: %+v", state)
	}
	local, _ = attachTest(t, session, true)
	if state := session.State(); state.Responder != local.ID {
		t.Fatalf("new local attachment was not preferred: %+v", state)
	}
	if err := session.WriteInput(browser, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := session.Wait(ctx)
	if err != nil || code != 7 {
		t.Fatalf("exit = %d, %v", code, err)
	}
	if err := session.WriteInput(browser, []byte("x")); !errors.Is(err, ErrSessionEnded) {
		t.Fatal(err)
	}
}

func TestDetachDoesNotStopSession(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	session.Detach(local)
	if err := session.WriteInput(local, []byte("detached\n")); err == nil {
		t.Fatal("detached attachment wrote to terminal")
	}
	if !session.State().Running || session.State().Responder != 0 {
		t.Fatal(session.State())
	}
	browser, _ := attachTest(t, session, false)
	if session.State().Responder != browser.ID {
		t.Fatalf("browser did not become responder: %+v", session.State())
	}
	if err := session.WriteInput(browser, []byte("still here\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, browser, "", "ECHO still here")
}

func TestSessionSerializesConcurrentLocalAndBrowserInput(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	browser, _ := attachTest(t, session, false)

	errs := make(chan error, 2)
	for _, source := range []struct {
		attachment *Attachment
		prefix     string
	}{{local, "local"}, {browser, "browser"}} {
		source := source
		go func() {
			for i := range 8 {
				if err := session.WriteInput(source.attachment, []byte(fmt.Sprintf("%s-%02d\n", source.prefix, i))); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	output := ""
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for strings.Count(output, "ECHO ") < 16 {
		select {
		case <-local.Ready:
			data, ok := local.ReadOutput()
			if !ok {
				t.Fatal("terminal ended during concurrent input")
			}
			output += string(data)
		case <-timer.C:
			t.Fatalf("received %d/16 echoed lines", strings.Count(output, "ECHO "))
		}
	}
	for _, prefix := range []string{"local-", "browser-"} {
		for i := range 8 {
			if !strings.Contains(output, fmt.Sprintf("ECHO %s%02d", prefix, i)) {
				t.Fatalf("input line missing after concurrent writes: %s%02d", prefix, i)
			}
		}
	}
}

func TestSlowObserverAndBoundedReplay(t *testing.T) {
	session := &Session{state: TerminalState{Running: true}, attachments: make(map[uint64]*Attachment)}
	slow, _, err := session.Attach(false)
	if err != nil {
		t.Fatal(err)
	}
	if session.State().Responder != slow.ID {
		t.Fatal("first browser was not chosen as responder")
	}
	chunk := bytes.Repeat([]byte("x"), chunkSize)
	for range replayLimit/chunkSize + 2 {
		session.publish(chunk)
	}
	if !errors.Is(session.AttachmentError(slow), ErrSlowObserver) {
		t.Fatal("lagging observer not disconnected")
	}
	if session.State().Responder != 0 {
		t.Fatal("disconnected observer kept responder slot")
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
	<-fresh.Ready
	data, _ := fresh.ReadOutput()
	if got := string(data); got != "next" {
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

func TestObserverRetainsTinyOutputBursts(t *testing.T) {
	session := &Session{state: TerminalState{Running: true}, attachments: make(map[uint64]*Attachment)}
	for _, local := range []bool{false, true} {
		attachment, _, err := session.Attach(local)
		if err != nil {
			t.Fatal(err)
		}
		var want bytes.Buffer
		for i := range 4096 {
			chunk := []byte(fmt.Sprintf("%d\r", i))
			want.Write(chunk)
			session.publish(chunk)
		}
		if err := session.AttachmentError(attachment); err != nil {
			t.Fatal(err)
		}
		<-attachment.Ready
		data, ok := attachment.ReadOutput()
		if !ok || !bytes.Equal(data, want.Bytes()) {
			t.Fatal("burst was disconnected, dropped, or reordered")
		}
		session.Detach(attachment)
	}
}
