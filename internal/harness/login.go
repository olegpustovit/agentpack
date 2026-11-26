package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// LoginFlow is how an agent signs in from the web UI: the agent's own login command, run in a
// terminal on this machine. The gateway shows the person the link (and code) the command prints,
// and types back what they paste. Nothing here opens a browser on the machine.
type LoginFlow struct {
	Command *exec.Cmd
	// Answers types each Keys once when its Screen appears before the link, in any order; a prompt
	// that never shows is skipped. Used to pick a menu entry.
	Answers []LoginAnswer
	// Code finds a one-time code printed next to the link (device flows). Nil when there is none.
	Code *regexp.Regexp
	// Input says what the person pastes back after signing in: "" (nothing), "code", or "url"
	// (the address the browser landed on).
	Input string
	// Verify reports whether sign-in succeeded, for a command that keeps running afterwards (a TUI
	// that goes on to its chat). It is checked after the person pastes their input. Nil means
	// success is a zero exit.
	Verify func(context.Context) bool
	// Finish runs after success, e.g. to install credentials the command wrote to an isolated home.
	Finish func() error
	// Cleanup runs once the attempt ends, whatever the outcome.
	Cleanup func()
}

type LoginAnswer struct{ Screen, Keys string }

// LoginMethod is one way to sign in. Agents with several providers (OMP, OpenCode) list one per provider.
type LoginMethod struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Account is what an agent reports about its sign-in on this machine. Label is what the person
// recognizes: an email, a plan or the signed-in providers. It never holds a token. Methods and
// SignOut are filled in by the gateway from the agent's registry entry.
type Account struct {
	Installed bool          `json:"installed"`
	SignedIn  bool          `json:"signed_in"`
	Label     string        `json:"label,omitempty"`
	Methods   []LoginMethod `json:"methods,omitempty"`
	SignOut   bool          `json:"sign_out,omitempty"` // the gateway can sign it out
	Error     string        `json:"error,omitempty"`
}

// ProbeAccount initializes the account and locates the CLI used to check its sign-in.
func ProbeAccount(target Target) (Account, string) {
	executable, err := target.Resolve()
	return Account{Installed: err == nil}, executable
}

// RunLogout runs an agent's own logout command and returns its output as the error when it fails.
func RunLogout(ctx context.Context, exe string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, exe, args...).CombinedOutput()
	if err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return errors.New(message)
		}
		return err
	}
	return nil
}

// RefreshToken sends an OAuth refresh_token grant as JSON and decodes the answer into reply. Errors
// name the status only: a token endpoint's body may echo credentials.
func RefreshToken(ctx context.Context, endpoint string, grant map[string]string, reply any) error {
	grant["grant_type"] = "refresh_token"
	body, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("token refresh answered %s", response.Status)
	}
	return json.NewDecoder(response.Body).Decode(reply)
}

// LoginSpec is how the gateway reads and signs in one agent on this machine.
type LoginSpec struct {
	Methods []LoginMethod
	Account func(context.Context) Account
	Login   func(method string) (LoginFlow, error)
	Logout  func(context.Context) error // nil when the agent has no way to sign out without a terminal
	// Credential is the file holding the agent's sign-in, shared with every node; nil when its
	// sign-in is not a file another machine can use (for example, OMP's database).
	Credential func() string
	// ValidCredential excludes incomplete or expired native credentials from sharing.
	ValidCredential func([]byte) bool
	// Freshness is when a sign-in's token was issued or expires, zero when unknown. It only vetoes
	// publishing a changed file that is older than the shared one; it never promotes a file.
	Freshness func([]byte) time.Time
	// FreshnessExpires says Freshness is when the token expires, not when it was issued.
	FreshnessExpires bool
	// Refresh renews a rotating sign-in ahead of the agent, returning nil when it is not due; only
	// Main calls it, or without a Main the machine that published the shared sign-in.
	Refresh func(context.Context, []byte) ([]byte, error)
}
