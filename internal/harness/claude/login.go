package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the ways the web UI signs Claude in.
var LoginMethods = []base.LoginMethod{{ID: "claudeai", Label: "Claude account"}, {ID: "console", Label: "Anthropic Console"}}

// Account reads `claude auth status --json`; it never reads the keychain itself.
func Account(ctx context.Context) base.Account {
	exe, err := base.LookPathEnv(base.Claude.Binary())
	if err != nil {
		return base.Account{}
	}
	account := base.Account{Installed: true}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: exe, Argv: []string{"auth", "status", "--json"}, Timeout: 15 * time.Second, Categories: []string{"auth"}, WriteScope: "none"})
	if err != nil {
		account.Error = err.Error()
		return account
	}
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal([]byte(result.Stdout), &status) != nil {
		account.Error = "claude auth status printed no status"
		return account
	}
	account.SignedIn, account.Label = status.LoggedIn, status.Email
	return account
}

// Logout runs `claude auth logout`.
func Logout(ctx context.Context) error {
	exe, err := base.LookPathEnv(base.Claude.Binary())
	if err != nil {
		return err
	}
	return base.RunLogout(ctx, exe, "auth", "logout")
}

// Login runs `claude auth login`. With BROWSER=true it opens nothing and prints the link; the page
// it leads to shows a code, which the person pastes back.
func Login(method string) (base.LoginFlow, error) {
	exe, err := base.LookPathEnv(base.Claude.Binary())
	if err != nil {
		return base.LoginFlow{}, err
	}
	flag := "--claudeai"
	if method == "console" {
		flag = "--console"
	}
	command := exec.Command(exe, "auth", "login", flag)
	command.Env = append(os.Environ(), "BROWSER=true")
	return base.LoginFlow{Command: command, Input: "code"}, nil
}
