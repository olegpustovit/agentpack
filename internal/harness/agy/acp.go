package agy

import base "github.com/olegpustovit/agentpack/internal/harness"

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	ctx.Arguments = []string{"acp"}
	command, err := launch(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	return base.ACPLaunch{Cmd: command}, nil
}
