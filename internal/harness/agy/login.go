package agy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Antigravity in.
var LoginMethods = []base.LoginMethod{{ID: "google", Label: "Google account"}}

// Account asks agy for its usage report, which only a signed-in agy can give.
func Account(ctx context.Context) base.Account {
	exe, err := base.LookPathEnv(base.Agy.Binary())
	if err != nil {
		return base.Account{}
	}
	account := base.Account{Installed: true}
	// A signed-out agy waits for a sign-in instead of answering, so no answer means signed out.
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: exe, Argv: []string{"-p", "/usage", "--output-format", "json"}, Timeout: 12 * time.Second, Categories: []string{"auth"}, Network: "agy_service", WriteScope: "none"})
	if err != nil || result.TimedOut {
		return account
	}
	var report usageReport
	account.SignedIn = result.ExitCode == 0 && json.Unmarshal([]byte(result.Stdout), &report) == nil && report.Status == "SUCCESS"
	return account
}

// Login drives agy's own sign-in screen: agy has no login command, and its ACP sign-in refuses to
// run without a local browser. The screen offers Google sign-in first, prints the link, and takes
// the address the browser lands on (antigravity.google/oauth-callback?…) pasted back.
func Login(string) (base.LoginFlow, error) {
	exe, err := base.LookPathEnv(base.Agy.Binary())
	if err != nil {
		return base.LoginFlow{}, err
	}
	// An empty folder, so the chat agy opens after sign-in sees no project.
	dir, err := os.MkdirTemp("", "agentpack-agy-login-")
	if err != nil {
		return base.LoginFlow{}, err
	}
	command := exec.Command(exe)
	command.Dir = dir
	return base.LoginFlow{
		Command: command,
		Answers: []base.LoginAnswer{{Screen: "Select login method", Keys: "\r"}},
		Input:   "url",
		Verify:  func(ctx context.Context) bool { return Account(ctx).SignedIn },
		Cleanup: func() { _ = os.RemoveAll(dir) },
	}, nil
}

// CredentialFile is agy's sign-in. On macOS agy prefers the keychain but reads this file when the
// keychain holds none, so a sign-in copied from another machine works there too.
func CredentialFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}
