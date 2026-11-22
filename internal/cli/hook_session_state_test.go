package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Codex's notify appends its JSON payload; session-state accepts it, and a notify the user configured
// (after --then) still runs with that payload. Reporting itself is best effort and never fails the hook.
func TestHookSessionStateRunsTheUsersCodexNotify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	out := filepath.Join(t.TempDir(), "payload")
	payload := `{"type":"agent-turn-complete","turn-id":"1"}`
	run := func(args ...string) int {
		t.Helper()
		runner := Runner{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
		code, err := runner.runHook(t.Context(), args)
		if err != nil && code == 0 {
			t.Fatal(err)
		}
		return code
	}
	base := []string{"session-state", "--target", "codex", "--session", "missing-session", "--state", "idle"}
	if code := run(append(base, payload)...); code != 0 {
		t.Fatalf("Codex payload rejected: exit %d", code)
	}
	if code := run(append(base, "--then", "sh", "-c", `printf %s "$1" > "$0"`, out, payload)...); code != 0 {
		t.Fatalf("chained notify: exit %d", code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, err := os.ReadFile(out); err == nil && string(data) == payload {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the user's notify did not receive the payload")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := run("session-state", "--target", "claude", "--session", "s", "--state", "idle", "stray"); code != 2 {
		t.Fatalf("a stray argument for Claude was accepted: exit %d", code)
	}
}
