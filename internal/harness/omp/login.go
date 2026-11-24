package omp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// LoginMethods are the subscriptions OMP signs in to with a browser link. Each prints its link;
// the ones that redirect to localhost get the landed-on address replayed on the machine.
var LoginMethods = []base.LoginMethod{
	{ID: "anthropic", Label: "Claude Pro/Max"},
	{ID: "openai-codex", Label: "ChatGPT Plus/Pro"},
	{ID: "google-gemini-cli", Label: "Gemini CLI"},
	{ID: "openrouter", Label: "OpenRouter"},
}

// Account lists the providers OMP can use: `omp models` lists only their models. OMP also finds
// logins it doesn't store (Claude Code's, for one), so its own credential table isn't enough.
// Running it refreshes OMP's model cache, as any omp run does.
func Account(ctx context.Context) base.Account {
	account, binary := base.ProbeAccount(base.OMP)
	if !account.Installed {
		return account
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"models", "--json", "--no-extensions"}, Timeout: 20 * time.Second, Categories: []string{"auth"}, WriteScope: "native_model_cache"})
	if err != nil {
		account.Error = err.Error()
		return account
	}
	var listed struct {
		Models []struct {
			Provider string `json:"provider"`
		} `json:"models"`
	}
	if json.Unmarshal([]byte(result.Stdout), &listed) != nil {
		account.Error = "omp models printed no list"
		return account
	}
	var providers []string
	for _, model := range listed.Models {
		if !slices.Contains(providers, model.Provider) {
			providers = append(providers, model.Provider)
		}
	}
	account.SignedIn, account.Label = len(providers) > 0, strings.Join(providers, ", ")
	return account
}

func Login(method string) (base.LoginFlow, error) {
	if !slices.ContainsFunc(LoginMethods, func(provider base.LoginMethod) bool { return provider.ID == method }) {
		return base.LoginFlow{}, fmt.Errorf("choose a provider to sign Oh My Pi in to")
	}
	binary, err := base.OMP.Resolve()
	if err != nil {
		return base.LoginFlow{}, err
	}
	return base.LoginFlow{Command: exec.Command(binary, "login", method), Input: "url"}, nil
}
