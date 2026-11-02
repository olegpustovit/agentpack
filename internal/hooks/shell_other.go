//go:build !windows

package hooks

import (
	"context"
	"os/exec"
)

func shellCommand(ctx context.Context, text string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", text)
}
