package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/harness"
)

// RunningEnvironment is the environment a running session had before a package change.
type RunningEnvironment struct {
	SessionID, DefinitionRoot, Workspace, Mode string
	Target                                     harness.Target
	Digest                                     string
}

// ChangedEnvironments returns the environments whose digest moved, after
// staging and validating each one, so no session is interrupted before every
// affected restart is ready.
func (service Service) ChangedEnvironments(ctx context.Context, before []RunningEnvironment) ([]RunningEnvironment, error) {
	var failures []error
	var changed []RunningEnvironment
	for _, environment := range before {
		after, err := EnvironmentDigest(environment.DefinitionRoot, environment.Workspace, environment.Mode, environment.Target)
		if err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but cannot validate session %s for restart: %w", environment.SessionID, err))
			continue
		}
		if environment.Digest != after {
			changed = append(changed, environment)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return nil, err
	}
	for _, environment := range changed {
		effective, _, err := service.SyncForLaunchOptions(ctx, environment.DefinitionRoot, SyncOptions{
			Mode: environment.Mode, Target: &environment.Target, WorkspaceRoot: environment.Workspace,
		})
		if err == nil {
			err = service.ValidateLaunchInputs(environment.DefinitionRoot, environment.Workspace, effective, environment.Target)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but session %s is not ready to restart: %w", environment.SessionID, err))
		}
	}
	return changed, errors.Join(failures...)
}
