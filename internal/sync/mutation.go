package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/paths"
	library "github.com/olegpustovit/agentpack/internal/registry"
)

// mutationHeld marks a context whose caller holds the mutation lock at path.
type mutationHeld struct{ path string }

// LockDefinition holds root's definition mutation lock, which serializes every
// writer of agentpack.toml and pack.lock and every rebuild, until release.
// Commands hold it through restart; launches release it after preparation,
// before running the agent. The returned context carries the hold, so a Sync
// called with it does not wait on its own caller; any other caller, in this
// process or another, waits. It also finishes an interrupted Library restore.
func LockDefinition(ctx context.Context, root string) (held context.Context, release func() error, err error) {
	path, err := paths.ProjectStateFile(root, "mutation.lock")
	if err != nil {
		return ctx, nil, err
	}
	key := mutationHeld{path}
	if ctx.Value(key) != nil {
		return ctx, func() error { return nil }, nil
	}
	if err := paths.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		return ctx, nil, err
	}
	// flock excludes other descriptors in this process too, so two goroutines serialize.
	lock := flock.New(path)
	acquired, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err == nil && !acquired {
		err = ctx.Err()
	}
	if err != nil {
		_ = lock.Close()
		return ctx, nil, fmt.Errorf("wait for project mutation: %w", err)
	}
	home, err := paths.UserAgentpackHome()
	if err == nil {
		err = library.RecoverDefinition(home, root)
	}
	if err != nil {
		_ = lock.Close()
		return ctx, nil, fmt.Errorf("recover interrupted library restore: %w", err)
	}
	return context.WithValue(ctx, key, true), lock.Unlock, nil
}
