package agy

import (
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	base "github.com/olegpustovit/agentpack/internal/harness"
)

// Antigravity's background-task wakeups stream session/update notifications with no
// session/prompt RPC and no idle signal; each model step ends with usage_update, and
// a final reply leaves it quiet (a tool-calling step follows with tool_call right away).
const unpromptedUsageSettle = 2 * time.Second

// Antigravity 1.5.0 added native ACP mid-turn steering by sending another session/prompt while a
// turn is running (https://antigravity.google/docs/ide/extensions/acp).
const antigravityACPServerName = "Antigravity ACP Server"

var minPromptSteerVersion = semver.MustParse("1.5.0")

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	ctx.Arguments = []string{"acp"}
	command, finish, err := launch(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	return base.ACPLaunch{
		Cmd:                    command,
		Finish:                 finish,
		ClientPermissionBypass: true,
		UnpromptedUsageSettle:  unpromptedUsageSettle,
		PromptSteers:           supportsPromptSteering,
	}, nil
}

func supportsPromptSteering(info base.ACPInitializeInfo) bool {
	if info.AgentName != antigravityACPServerName && !hasAntigravityMeta(info.CapabilitiesMeta) {
		return false
	}
	version, err := semver.NewVersion(strings.TrimPrefix(strings.TrimSpace(info.AgentVersion), "v"))
	if err != nil {
		return false
	}
	return !version.LessThan(minPromptSteerVersion)
}

func hasAntigravityMeta(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	_, ok := meta["antigravity"]
	return ok
}
