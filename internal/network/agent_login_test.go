//go:build linux || darwin

package network

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/harness"
)

// TestLoginChild plays an agent's login command: a menu, a styled link and code, then a paste.
func TestLoginChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_LOGIN_CHILD") != "1" {
		return
	}
	if os.Getenv("AGENTPACK_TEST_LOGIN_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
	}
	input := bufio.NewReader(os.Stdin)
	fmt.Print("Select login method:\r\n > 1. Google\r\n")
	// The terminal turns the typed \r into \n.
	if line, _ := input.ReadString('\n'); strings.TrimSpace(line) != "" {
		os.Exit(3)
	}
	// An OSC 8 hyperlink and colours, as real CLIs print them.
	fmt.Print("Visit \x1b]8;;https://auth.example/device?x=1\x07https://auth.example/device?x=1\x1b]8;;\x07\r\n")
	fmt.Print("Code: \x1b[94mK7QF-92XD\x1b[0m\r\nPaste code here > ")
	line, _ := input.ReadString('\n')
	if strings.TrimSpace(line) != "good-code" {
		// The echoed paste comes last; it must never become the reported error.
		fmt.Printf("\r\nLogin failed: bad code\r\nYou entered: %s\r\n", strings.TrimSpace(line))
		os.Exit(1)
	}
	if os.Getenv("AGENTPACK_TEST_LOGIN_HANG") == "1" {
		select {}
	}
	os.Exit(0)
}

func loginChild(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestLoginChild$")
	command.Env = append(append(os.Environ(), "AGENTPACK_TEST_LOGIN_CHILD=1"), env...)
	return command
}

func finishedLogin(t *testing.T, attempt *loginAttempt) loginStep {
	t.Helper()
	select {
	case <-attempt.done:
	case <-time.After(10 * time.Second):
		t.Fatal("sign-in did not end")
	}
	return attempt.current()
}

func TestLoginShowsLinkAndCodeThenFinishes(t *testing.T) {
	var finished, cleaned atomic.Bool
	attempt, err := startLogin(harness.LoginFlow{
		Command: loginChild(t),
		Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}},
		Code:    regexp.MustCompile(`[A-Z0-9]{4}-[A-Z0-9]{4}`),
		Input:   "code",
		Finish:  func() error { finished.Store(true); return nil },
		Cleanup: func() { cleaned.Store(true) },
	})
	if err != nil {
		t.Fatal(err)
	}
	step, err := attempt.waitLink(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if step.URL != "https://auth.example/device?x=1" || step.Code != "K7QF-92XD" || step.Input != "code" || step.State != "waiting" {
		t.Fatalf("step = %+v", step)
	}
	if err := attempt.Submit(context.Background(), "good-code"); err != nil {
		t.Fatal(err)
	}
	if step := finishedLogin(t, attempt); step.State != "done" || !finished.Load() || !cleaned.Load() {
		t.Fatalf("step = %+v finished=%v cleaned=%v", step, finished.Load(), cleaned.Load())
	}
}

func TestLoginFailureNamesTheCommandsReason(t *testing.T) {
	attempt, err := startLogin(harness.LoginFlow{Command: loginChild(t), Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}}, Input: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.waitLink(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := attempt.Submit(context.Background(), "wrong"); err != nil {
		t.Fatal(err)
	}
	if step := finishedLogin(t, attempt); step.State != "failed" || step.Error != "Login failed: bad code" {
		t.Fatalf("step = %+v", step)
	}
	if err := attempt.Submit(context.Background(), "again"); err == nil {
		t.Fatal("an ended sign-in accepted input")
	}
}

// A pasted localhost address is fetched on the machine; a running TUI is then verified and stopped.
func TestLoginReplaysLocalhostCallbackAndVerifies(t *testing.T) {
	interval := loginVerifyInterval
	loginVerifyInterval = 10 * time.Millisecond
	t.Cleanup(func() { loginVerifyInterval = interval })
	var hit atomic.Bool
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(r.URL.Query().Get("code") == "abc")
	}))
	defer callback.Close()
	attempt, err := startLogin(harness.LoginFlow{
		Command: loginChild(t, "AGENTPACK_TEST_LOGIN_HANG=1"),
		Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}},
		Input:   "url",
		Verify:  func(context.Context) bool { return hit.Load() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.waitLink(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := attempt.Submit(context.Background(), callback.URL+"/callback?code=abc"); err != nil {
		t.Fatal(err)
	}
	if step := finishedLogin(t, attempt); step.State != "done" || !hit.Load() {
		t.Fatalf("step = %+v hit=%v", step, hit.Load())
	}
}

func TestLoginRejectsControlCharacters(t *testing.T) {
	attempt := &loginAttempt{step: loginStep{State: "waiting"}}
	if err := attempt.Submit(context.Background(), "code\x03"); err == nil {
		t.Fatal("control characters were typed into the login command")
	}
}

// A login command that ignores SIGTERM (an npm wrapper's child, say) is still ended by Cancel.
func TestLoginCancelKillsACommandThatIgnoresTerm(t *testing.T) {
	grace := loginStopGrace
	loginStopGrace = 50 * time.Millisecond
	t.Cleanup(func() { loginStopGrace = grace })
	attempt, err := startLogin(harness.LoginFlow{Command: loginChild(t, "AGENTPACK_TEST_LOGIN_IGNORE_TERM=1"), Answers: []harness.LoginAnswer{{Screen: "Select login method", Keys: "\r"}}, Input: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.waitLink(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt.Cancel()
	if step := finishedLogin(t, attempt); step.State != "failed" || step.Error != "sign-in was cancelled" {
		t.Fatalf("step = %+v", step)
	}
}
