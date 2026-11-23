package network

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/olegpustovit/agentpack/internal/session"
)

// A failed reload leaves the chat open and the reload pending, so its next prompt retries it.
func TestFailedReloadKeepsTheChatResumable(t *testing.T) {
	session, _ := newFakeAgentSession(t)
	worker := &WorkerHTTP{Conversation: session, SessionDir: t.TempDir(), Diagnostics: t.Context(), done: make(chan struct{})}
	worker.restartACP = func(context.Context) error { return errors.New("adapter failed") }
	if err := worker.Restart(t.Context(), "reload-operation-0001"); err == nil {
		t.Fatal("restart reported success")
	}
	if worker.Finished || !worker.reloadPending() || !worker.reloadFailed.Load() {
		t.Fatalf("finished=%v pending=%v failed=%v", worker.Finished, worker.reloadPending(), worker.reloadFailed.Load())
	}
	worker.restartACP = func(context.Context) error { return nil }
	if reloaded, err := worker.reloadBeforePrompt(t.Context()); err != nil || !reloaded || worker.reloadPending() {
		t.Fatalf("retry: reloaded=%v err=%v pending=%v", reloaded, err, worker.reloadPending())
	}
}

// A terminal reload waits for the turn its agent's hooks report, then resumes the agent's own session.
func TestTerminalReloadWaitsForIdleAndResumes(t *testing.T) {
	worker := &WorkerHTTP{SessionDir: t.TempDir(), Diagnostics: t.Context(), done: make(chan struct{})}
	worker.agentState.Store(string(StateWorking))
	if deferred, err := worker.reloadWhenIdle(t.Context(), "reload-operation-0002"); err != nil || !deferred || !worker.reloadPending() {
		t.Fatalf("deferred=%v err=%v pending=%v", deferred, err, worker.reloadPending())
	}
	launch := WorkerLaunch{Arguments: []string{"--workspace", "/w", "claude"}, Session: session.SessionInfo{Harness: "claude"}}
	if got := resumeArguments(launch, func() string { return "" }); !slices.Equal(got, launch.Arguments) {
		t.Fatalf("first launch = %v", got)
	}
	if got := resumeArguments(launch, func() string { return "abc" }); !slices.Equal(got, []string{"--workspace", "/w", "claude", "--resume", "abc"}) {
		t.Fatalf("claude resume = %v", got)
	}
	launch.Session.Harness = "codex"
	if got := resumeArguments(launch, func() string { return "abc" }); !slices.Equal(got[len(got)-2:], []string{"resume", "abc"}) {
		t.Fatalf("codex resume = %v", got)
	}
	launch.Session.Harness = "opencode"
	if got := resumeArguments(launch, func() string { return "abc" }); len(got) != 3 {
		t.Fatalf("an agent without a resume by ID got %v", got)
	}
}
