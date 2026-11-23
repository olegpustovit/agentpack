package registry

import (
	"context"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/cursor"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
	"github.com/olegpustovit/agentpack/internal/hooks"
)

// Entry is everything agentpack knows about one agent. Nil fields mean the agent lacks that
// feature: Grok has no hook renderer, and only Claude, Codex and OpenCode can be probed.
type Entry struct {
	base.Harness
	Hooks  hooks.Renderer
	Login  *base.LoginSpec
	Limits func(context.Context) base.LimitSnapshot
	// Probe runs the native agent against current staging and records what it discovered.
	Probe func(context.Context, base.ProbeRequest) (environment.Receipt, error)
}

func All() []Entry {
	return []Entry{
		{Harness: claude.New(), Hooks: claude.HookRenderer{}, Limits: claude.ReadLimits, Probe: claude.Probe,
			Login: &base.LoginSpec{Methods: claude.LoginMethods, Account: claude.Account, Login: claude.Login, Logout: claude.Logout, Credential: claude.CredentialFile, Refresh: claude.RefreshCredential}},
		{Harness: cursor.New(), Hooks: cursor.HookRenderer{}, Limits: cursor.ReadLimits,
			Login: &base.LoginSpec{Methods: cursor.LoginMethods, Account: cursor.Account, Login: cursor.Login, Logout: cursor.Logout}},
		{Harness: codex.New(), Hooks: codex.HookRenderer{}, Limits: func(ctx context.Context) base.LimitSnapshot { return codex.ReadLimits(ctx, "") }, Probe: codex.Probe,
			Login: &base.LoginSpec{Methods: codex.LoginMethods, Account: codex.Account, Login: codex.Login, Logout: codex.Logout, Credential: codex.CredentialFile, Refresh: codex.RefreshCredential}},
		{Harness: opencode.New(), Hooks: opencode.HookRenderer{}, Limits: opencode.ReadLimits, Probe: opencode.Probe,
			Login: &base.LoginSpec{Methods: opencode.LoginMethods, Account: opencode.Account, Login: opencode.Login, Credential: opencode.CredentialFile}},
		{Harness: grok.New(), Limits: grok.ReadLimits,
			Login: &base.LoginSpec{Methods: grok.LoginMethods, Account: grok.Account, Login: grok.Login, Logout: grok.Logout, Credential: grok.CredentialFile}},
		{Harness: agy.New(), Hooks: agy.HookRenderer{}, Limits: agy.ReadLimits,
			Login: &base.LoginSpec{Methods: agy.LoginMethods, Account: agy.Account, Login: agy.Login, Credential: agy.CredentialFile}},
		{Harness: omp.New(), Hooks: omp.HookRenderer{}, Limits: omp.ReadLimits,
			Login: &base.LoginSpec{Methods: omp.LoginMethods, Account: omp.Account, Login: omp.Login}},
	}
}

func ByTarget(target base.Target) (Entry, error) {
	for _, entry := range All() {
		if entry.Target == target {
			return entry, nil
		}
	}
	return Entry{}, fmt.Errorf("unknown harness %s", target)
}
