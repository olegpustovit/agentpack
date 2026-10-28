package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

type runningEnvironment struct {
	session     network.SessionInfo
	digest      string
	target      harness.Target
	operationID string
}

func snapshotRunningEnvironments(ctx context.Context, definitionRoot string) ([]runningEnvironment, error) {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return nil, err
	}
	sessions, err := network.Sessions(ctx, home)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	root, err := os.Stat(definitionRoot)
	if err != nil {
		return nil, err
	}
	var snapshots []runningEnvironment
	for _, session := range sessions {
		if !session.State.Interactive || !session.State.Running || session.State.Restarting {
			continue
		}
		definition := session.DefinitionRoot
		if definition == "" {
			definition = session.Directory
		}
		info, err := os.Stat(definition)
		if err != nil || !os.SameFile(root, info) {
			continue
		}
		session.DefinitionRoot = definitionRoot
		name := session.Harness
		if name == "agent" {
			name = "cursor"
		}
		target, err := harness.ParseTarget(name)
		if err != nil {
			return nil, err
		}
		digest, err := network.SessionEnvironment(home, session.ID)
		if err == nil && digest == "" {
			digest, err = packSync.EnvironmentDigest(definitionRoot, session.Directory, session.Mode, target)
		}
		if err != nil {
			return nil, fmt.Errorf("check running session %s before package change: %w", session.ID, err)
		}
		snapshots = append(snapshots, runningEnvironment{session: session, digest: digest, target: target, operationID: rand.Text()})
	}
	return snapshots, nil
}

func (runner Runner) restartChangedEnvironments(ctx context.Context, snapshots []runningEnvironment) error {
	if len(snapshots) == 0 {
		return nil
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	live, err := network.Sessions(ctx, home)
	if err != nil {
		return err
	}
	running := make(map[string]bool, len(live))
	for _, session := range live {
		running[session.ID] = session.State.Interactive && session.State.Running && !session.State.Restarting
	}
	var failures []error
	var changed []runningEnvironment
	for _, before := range snapshots {
		if !running[before.session.ID] {
			continue
		}
		after, err := packSync.EnvironmentDigest(before.session.DefinitionRoot, before.session.Directory, before.session.Mode, before.target)
		if err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but cannot validate session %s for restart: %w", before.session.ID, err))
			continue
		}
		if before.digest == after {
			continue
		}
		changed = append(changed, before)
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	// Prepare every affected environment before interrupting any conversation.
	for _, before := range changed {
		effective, _, err := runner.Service.SyncForLaunchOptions(ctx, before.session.DefinitionRoot, packSync.SyncOptions{
			Mode: before.session.Mode, Target: &before.target, WorkspaceRoot: before.session.Directory,
		})
		if err == nil {
			err = runner.Service.ValidateLaunchInputs(before.session.DefinitionRoot, before.session.Directory, effective, before.target)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but session %s is not ready to restart: %w", before.session.ID, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	for _, before := range changed {
		if err := network.RestartWorker(ctx, home, before.session.ID, before.operationID); err != nil {
			failures = append(failures, fmt.Errorf("package change saved, but session %s could not restart: %w", before.session.ID, err))
			continue
		}
		fmt.Fprintf(runner.Stderr, "agentpack: restarted %s in %s (mode %s) after its environment changed\n", before.session.Harness, before.session.Directory, before.session.Mode)
	}
	return errors.Join(failures...)
}

func (runner Runner) installNetworkPackage(ctx context.Context, directory, spec string, restart bool) error {
	packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
	if err != nil {
		return err
	}
	mutation, err := acquireDefinitionMutation(ctx, packContext.DefinitionRoot)
	if err != nil {
		return err
	}
	defer mutation.Unlock()
	var before []runningEnvironment
	if restart {
		before, err = snapshotRunningEnvironments(ctx, packContext.DefinitionRoot)
		if err != nil {
			return err
		}
	}
	if _, err := runner.Service.AddAll(ctx, packContext.DefinitionRoot, spec, false); err != nil {
		return err
	}
	if err := captureNetworkEnvironment(packContext.DefinitionRoot, directory, ""); err != nil {
		return fmt.Errorf("package installed, but library capture failed: %w", err)
	}
	return runner.restartChangedEnvironments(ctx, before)
}
