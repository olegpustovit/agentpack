package codex

import (
	"context"
	"os"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const codexACPPackage = "@agentclientprotocol/codex-acp@2.5.0"

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	binary, err := base.EnsureACPAdapterExecutable(context.Background(), codexACPPackage)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	home, lease, err := acquireGenerationLease(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return base.ACPLaunch{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.Unlock()
		}
	}()
	if needsShortHome(home) {
		if shortDir, err := shortStagingCodexHome(ctx.ProjectRoot, ctx.Mode.Name()); err == nil {
			home = shortDir
		}
	}
	command := base.ACPAdapterCommand(binary)
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	pendingGenerationLeases.Store(command, lease)
	keepLease = true
	return base.ACPLaunch{Cmd: command}, nil
}
