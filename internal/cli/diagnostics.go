package cli

import (
	"context"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
	"github.com/spf13/cobra"
)

func (runner Runner) recordFailure(ctx context.Context, root *cobra.Command, arguments []string, code int, failure error) {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return
	}
	spool, err := obs.OpenSpool(home, obs.SpoolOptions{Proc: "cli"})
	if err != nil {
		return
	}
	defer spool.Close()
	command, _, _ := root.Find(arguments)
	if command == nil {
		command = root
	}
	args := packSync.RedactCredentialArgs(arguments)
	if _, ok := harness.ParseLauncher(command.Name()); ok {
		args = []string{command.Name(), "<harness arguments omitted>"}
	}
	for parent := command; parent != nil; parent = parent.Parent() {
		if parent.Name() == "gateway" {
			args = []string{command.CommandPath(), "<gateway arguments omitted>"}
			break
		}
	}
	_, event := obs.Start(obs.WithSink(ctx, spool, obs.BuildMetadata(Version, "local", "cli")), "cli", command.CommandPath())
	event.Status = code
	event.Set("args", args)
	if failure == nil {
		failure = fmt.Errorf("command exited with status %d", code)
	}
	event.Fail(failure)
	event.End()
}
