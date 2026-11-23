package agy

import (
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// Antigravity's background-task wakeups stream session/update notifications with no
// session/prompt RPC and no idle signal; each model step ends with usage_update, and
// a final reply leaves it quiet (a tool-calling step follows with tool_call right away).
const unpromptedUsageSettle = 2 * time.Second

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
	}, nil
}
