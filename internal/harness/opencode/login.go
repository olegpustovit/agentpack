package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the subscriptions OpenCode signs in to with a browser link. The first login
// method each offers is its browser sign-in, so the method menu is answered with Enter.
var LoginMethods = []base.LoginMethod{
	{ID: "openai", Label: "ChatGPT Plus/Pro"},
	{ID: "github-copilot", Label: "GitHub Copilot"},
}

var copilotCode = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)

// Account lists the providers OpenCode holds credentials for: v2 reports them as JSON, v1 keeps
// them as the keys of its auth.json.
func Account(ctx context.Context) base.Account {
	exe, err := base.ResolveBinary(base.OpenCode.Binary())
	if err != nil {
		return base.Account{}
	}
	account := base.Account{Installed: true}
	var providers []string
	switch nativeMajor() {
	case 2:
		result, err := base.RunProbe(ctx, base.ProbePlan{Executable: exe, Argv: []string{"auth", "list", "--format", "json"}, Timeout: 15 * time.Second, Categories: []string{"auth"}, WriteScope: "none"})
		if err != nil {
			account.Error = err.Error()
			return account
		}
		var listed []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(result.Stdout), &listed) != nil {
			account.Error = "opencode auth list printed no list"
			return account
		}
		for _, integration := range listed {
			providers = append(providers, integration.ID)
		}
	default:
		data, err := os.ReadFile(CredentialFile())
		var stored map[string]json.RawMessage
		if err == nil && json.Unmarshal(data, &stored) == nil {
			for id := range stored {
				providers = append(providers, id)
			}
			sort.Strings(providers)
		}
	}
	account.SignedIn, account.Label = len(providers) > 0, strings.Join(providers, ", ")
	return account
}

// CredentialFile is OpenCode's provider sign-ins, shared with other machines by the gateway.
func CredentialFile() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, _ := os.UserHomeDir()
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "opencode", "auth.json")
}

// Login runs `opencode auth login` for one provider. OpenAI redirects to localhost, so the landed-on
// address is pasted (and replayed on the machine); Copilot is a device code for GitHub.com.
// Anthropic is absent: OpenCode 1.19 takes only an API key for it.
func Login(method string) (base.LoginFlow, error) {
	if !slices.ContainsFunc(LoginMethods, func(provider base.LoginMethod) bool { return provider.ID == method }) {
		return base.LoginFlow{}, fmt.Errorf("choose a provider to sign OpenCode in to")
	}
	exe, err := base.ResolveBinary(base.OpenCode.Binary())
	if err != nil {
		return base.LoginFlow{}, err
	}
	arguments := []string{"auth", "login", "--provider", method}
	if nativeMajor() == 2 {
		arguments = []string{"auth", "login", method}
	}
	flow := base.LoginFlow{
		Command: exec.Command(exe, arguments...),
		// v1 asks "Login method", v2 "Select login method".
		Answers: []base.LoginAnswer{{Screen: "ogin method", Keys: "\r"}},
	}
	switch method {
	case "openai":
		flow.Input = "url"
	case "github-copilot":
		flow.Answers = append(flow.Answers, base.LoginAnswer{Screen: "GitHub deployment type", Keys: "\r"})
		flow.Code = copilotCode
	}
	return flow, nil
}
