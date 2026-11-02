package hooks

import (
	"context"
	"os/exec"
	"syscall"
)

// cmd.exe reads its command line as written. Go's argument quoting turns each
// quote into \", which cmd passes on and breaks `node -e "..."`, so the line is
// given whole.
func shellCommand(ctx context.Context, text string) *exec.Cmd {
	command := exec.CommandContext(ctx, "cmd")
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /S /C "` + text + `"`}
	return command
}
