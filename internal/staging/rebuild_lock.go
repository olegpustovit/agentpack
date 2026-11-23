package staging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// AcquirePublisher serializes builders, not the sessions using published files.
func AcquirePublisher(ctx context.Context, projectRoot, modeName string) (*flock.Flock, error) {
	path, err := paths.ProjectStateFile(projectRoot, "staging-"+paths.ModePathComponent(modeName)+".lock")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	acquired, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("wait for staging publisher: %w", err)
	}
	if !acquired {
		return nil, fmt.Errorf("wait for staging publisher: %w", ctx.Err())
	}
	return lock, nil
}
