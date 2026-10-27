package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// Mutations hold this through restart; launches release it after preparation,
// before running the agent. Worker restart never waits for launch preparation.
func acquireDefinitionMutation(ctx context.Context, root string) (*flock.Flock, error) {
	path, err := paths.ProjectStateFile(root, "mutation.lock")
	if err != nil {
		return nil, err
	}
	if err := network.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	acquired, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("wait for project mutation: %w", err)
	}
	if !acquired {
		_ = lock.Close()
		return nil, fmt.Errorf("wait for project mutation: %w", ctx.Err())
	}
	return lock, nil
}

func definitionMutation(invocation Invocation) bool {
	switch invocation.Command {
	case "add", "remove", "update", "lock":
		return true
	case "sync":
		return !hasBeforeDoubleDash(invocation.Args, "--dry-run", "--verify-only")
	case "mcp":
		return first(invocation.Args) == "add" || first(invocation.Args) == "remove"
	case "mode":
		switch first(invocation.Args) {
		case "create", "delete", "enable", "disable", "base", "tui":
			return true
		}
	case "env":
		return first(invocation.Args) == "restore"
	case "eject":
		return !hasBeforeDoubleDash(invocation.Args, "--remove")
	}
	return false
}
