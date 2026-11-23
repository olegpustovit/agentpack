package gateway

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
)

const (
	// Wide enough that a TUI never wraps a sign-in link (Google's run to ~700 characters).
	loginColumns = 1000
	loginRows    = 50
	// How long a login command may take to print its link.
	loginLinkTimeout = 30 * time.Second
	// How long a person has to finish signing in.
	loginLifetime = 15 * time.Minute
	// Raw output kept for parsing; links and prompts come early.
	loginOutputLimit = 256 << 10
)

// How often a still-running login command's sign-in is checked after input, and how long it gets
// to exit when stopped; tests shorten both.
var (
	loginVerifyInterval = 3 * time.Second
	loginStopGrace      = 3 * time.Second
)

var loginURL = regexp.MustCompile(`https?://[^\s"'<>\x07\x1b]+`)

// loginStep is what the person needs to sign in. URL and Code are shown, never logged.
type loginStep struct {
	ID    string `json:"id"`
	URL   string `json:"url,omitempty"`
	Code  string `json:"code,omitempty"`
	Input string `json:"input,omitempty"`
	State string `json:"state"` // "waiting", "done" or "failed"
	Error string `json:"error,omitempty"`
}

// loginAttempt runs one agent's login command in a terminal until it succeeds, fails, is cancelled
// or expires.
type loginAttempt struct {
	flow    harness.LoginFlow
	session *network.Session
	attach  *network.Attachment
	cancel  context.CancelFunc
	// Read once at start, so goroutines never race a test that changes the defaults.
	grace, verifyEvery time.Duration

	mu       sync.Mutex
	raw      []byte
	answered []bool   // by flow answer; each prompt is answered once, in whatever order it appears
	pasted   []string // what the person typed; never repeated in an error
	verified atomic.Bool
	step     loginStep
	found    chan struct{} // closed once the link is known or the attempt ended
	done     chan struct{} // closed once the attempt ended
	once     sync.Once
}

func startLogin(flow harness.LoginFlow) (*loginAttempt, error) {
	if flow.Command == nil {
		return nil, errors.New("this agent has no login command")
	}
	session, err := network.StartSession(flow.Command, loginRows, loginColumns)
	if err != nil {
		if flow.Cleanup != nil {
			flow.Cleanup()
		}
		return nil, err
	}
	attach, replay, err := session.Attach(false, 0)
	if err != nil {
		_ = stopLogin(session, loginStopGrace)
		if flow.Cleanup != nil {
			flow.Cleanup()
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginLifetime)
	attempt := &loginAttempt{flow: flow, session: session, attach: attach, cancel: cancel, grace: loginStopGrace, verifyEvery: loginVerifyInterval, found: make(chan struct{}), done: make(chan struct{}),
		step: loginStep{ID: "login-" + rand.Text(), Input: flow.Input, State: "waiting"}}
	go attempt.run(ctx, replay.Data)
	return attempt, nil
}

// waitLink returns the step once the command printed its link, or the attempt ended first.
func (a *loginAttempt) waitLink(ctx context.Context) (loginStep, error) {
	timer := time.NewTimer(loginLinkTimeout)
	defer timer.Stop()
	select {
	case <-a.found:
	case <-timer.C:
		a.Cancel()
		return loginStep{}, errors.New("the login command printed no sign-in link")
	case <-ctx.Done():
		return loginStep{}, ctx.Err()
	}
	return a.current(), nil
}

func (a *loginAttempt) current() loginStep {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.step
}

func (a *loginAttempt) run(ctx context.Context, replay []byte) {
	defer a.cancel()
	defer func() {
		if a.flow.Cleanup != nil {
			a.flow.Cleanup()
		}
	}()
	go func() {
		<-ctx.Done()
		_ = stopLogin(a.session, a.grace)
	}()
	a.observe(replay)
	for open := true; open; {
		_, open = <-a.attach.Ready
		data, _ := a.attach.ReadOutput()
		a.observe(data)
	}
	code, err := a.session.Wait(context.Background())
	switch {
	case a.verified.Load() || err == nil && code == 0 && a.flow.Verify == nil:
		err = nil
		if a.flow.Finish != nil {
			err = a.flow.Finish()
		}
	case ctx.Err() == context.DeadlineExceeded:
		err = errors.New("sign-in timed out")
	case ctx.Err() != nil:
		err = errors.New("sign-in was cancelled")
	case err == nil:
		err = a.failure(code)
	}
	a.mu.Lock()
	if err != nil {
		a.step.State, a.step.Error = "failed", err.Error()
	} else {
		a.step.State = "done"
	}
	a.mu.Unlock()
	a.once.Do(func() { close(a.found) })
	close(a.done)
}

// observe adds output and acts on it: answers prompts and picks up the link and code.
func (a *loginAttempt) observe(data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.raw = append(a.raw, data...)
	if extra := len(a.raw) - loginOutputLimit; extra > 0 {
		a.raw = a.raw[extra:]
	}
	// Prompts come before the link, so once it is found a redrawing TUI only fills the buffer for failure().
	if a.step.URL != "" && (a.flow.Code == nil || a.step.Code != "") {
		return
	}
	// Strip the whole buffer: an escape sequence can be split across reads.
	screen := ansi.Strip(string(a.raw))
	if a.answered == nil {
		a.answered = make([]bool, len(a.flow.Answers))
	}
	for i, answer := range a.flow.Answers {
		if !a.answered[i] && strings.Contains(screen, answer.Screen) {
			_ = a.session.WriteInput(a.attach, []byte(answer.Keys))
			a.answered[i] = true
		}
	}
	if a.step.URL == "" {
		a.step.URL = loginURL.FindString(screen)
	}
	if a.step.URL != "" && a.flow.Code != nil && a.step.Code == "" {
		a.step.Code = a.flow.Code.FindString(screen)
	}
	if a.step.URL != "" && (a.flow.Code == nil || a.step.Code != "") {
		a.once.Do(func() { close(a.found) })
	}
}

// failure names why the command exited: its last meaningful line, never one holding the link,
// the code or anything the person pasted, since it is recorded in diagnostics.
func (a *loginAttempt) failure(code int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	lines := strings.Split(ansi.Strip(string(a.raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		secret := a.step.Code != "" && strings.Contains(line, a.step.Code) || slices.ContainsFunc(a.pasted, func(text string) bool { return strings.Contains(line, text) })
		if line != "" && !loginURL.MatchString(line) && !secret && len(line) < 300 {
			return errors.New(line)
		}
	}
	return fmt.Errorf("the login command exited with code %d", code)
}

// Submit types what the person pasted. A pasted http://localhost address is a callback the
// person's browser could not reach, so it is replayed here, on the machine running the command.
func (a *loginAttempt) Submit(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 8192 || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return errors.New("paste the code or address shown in your browser")
	}
	a.mu.Lock()
	waiting := a.step.State == "waiting"
	if waiting {
		a.pasted = append(a.pasted, text)
	}
	a.mu.Unlock()
	if !waiting {
		return errors.New("this sign-in has ended")
	}
	var err error
	if target, ok := loopbackURL(text); ok {
		err = replayCallback(ctx, target)
	} else {
		err = a.session.WriteInput(a.attach, []byte(text+"\r"))
	}
	if err == nil && a.flow.Verify != nil {
		go a.verify()
	}
	return err
}

// verify checks a still-running command's sign-in for a while after input, and ends it on success.
func (a *loginAttempt) verify() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ticker := time.NewTicker(a.verifyEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		case <-ticker.C:
			if a.flow.Verify(ctx) {
				a.verified.Store(true)
				_ = stopLogin(a.session, a.grace)
				return
			}
		}
	}
}

func (a *loginAttempt) Cancel() { a.cancel() }

func loopbackURL(text string) (*url.URL, bool) {
	target, err := url.Parse(text)
	if err != nil || target.Scheme != "http" || target.User != nil {
		return nil, false
	}
	host := target.Hostname()
	if host == "localhost" {
		return target, true
	}
	ip := net.ParseIP(host)
	return target, ip != nil && ip.IsLoopback()
}

func replayCallback(ctx context.Context, target *url.URL) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("the login command is no longer waiting for that address")
	}
	response.Body.Close()
	if response.StatusCode >= 400 {
		return fmt.Errorf("the login command rejected that address (%d)", response.StatusCode)
	}
	return nil
}

// stopLogin ends a login command, killing its process group if it ignores the polite signal: npm
// wrappers such as opencode's leave their child running otherwise.
func stopLogin(session *network.Session, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := session.Stop(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}
