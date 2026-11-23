package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// networkLibrary wires the Library's operations to the gateway; the logic lives in registry.
func (runner Runner) networkLibrary(home string) (gateway.LibraryOptions, error) {
	store, err := registry.New(home)
	if err != nil {
		return gateway.LibraryOptions{}, err
	}
	node, err := os.Hostname()
	if err != nil {
		return gateway.LibraryOptions{}, errors.Join(err, store.Close())
	}
	bootstrap := &registry.Bootstrapper{
		Store: store, Home: home, Node: node,
		Recent: func() ([]registry.RecentProject, error) {
			sessions, err := network.RecentIn(context.Background(), store.StateStore().Reader)
			recent := make([]registry.RecentProject, len(sessions))
			for i, session := range sessions {
				recent[i] = registry.RecentProject{Directory: session.Directory, DefinitionRoot: session.DefinitionRoot, Mode: session.Mode}
			}
			return recent, err
		},
		Definition: func(workspace, definition string) (string, error) {
			packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: workspace, DefinitionRoot: definition}}, true)
			return packContext.DefinitionRoot, err
		},
	}
	definitionOf := func(directory string) (string, error) {
		return bootstrap.Definition(directory, "")
	}
	return gateway.LibraryOptions{
		Store:     store,
		Bootstrap: bootstrap.Run,
		DraftState: func(ctx context.Context, draft registry.Draft) (registry.DraftState, error) {
			definition, err := definitionOf(draft.Directory)
			if err != nil {
				return registry.DraftState{Pins: map[string]registry.PackagePin{}}, err
			}
			return store.DraftStateOf(draft, definition)
		},
		Save: func(ctx context.Context, input gateway.SaveAgentpackInput) (gateway.SavedAgentpack, error) {
			definition, err := definitionOf(input.Directory)
			if err != nil {
				return gateway.SavedAgentpack{}, err
			}
			held, release, err := packSync.LockDefinition(ctx, definition)
			if err != nil {
				return gateway.SavedAgentpack{}, err
			}
			defer release()
			ctx = held
			capture, err := registry.ProjectCapture(definition, input.Directory, node, input.Mode)
			if err != nil {
				return gateway.SavedAgentpack{}, err
			}
			capture.ID, capture.Name, capture.Label = input.AgentpackID, input.Name, input.Label
			pack, revision, err := store.Capture(capture)
			if err == nil {
				err = store.SetCapture(definition, pack.ID, input.AutoCapture)
			}
			return gateway.SavedAgentpack{Agentpack: pack, Revision: revision}, err
		},
		Preview: func(ctx context.Context, id, revision, directory string) (registry.DiffResult, error) {
			definition, err := definitionOf(directory)
			if err != nil {
				return registry.DiffResult{}, err
			}
			_, diff, err := store.Preview(id, revision, definition)
			return diff, err
		},
		Restore: func(ctx context.Context, id, revision, directory string, restart bool) error {
			definition, err := definitionOf(directory)
			if err != nil {
				return err
			}
			held, release, err := packSync.LockDefinition(ctx, definition)
			if err != nil {
				return err
			}
			defer release()
			ctx = held
			selected, _, err := store.Preview(id, revision, definition)
			if err != nil {
				return err
			}
			var before []runningEnvironment
			if restart {
				before, err = snapshotRunningEnvironments(ctx, definition)
				if err != nil {
					return err
				}
			}
			if err := store.Restore(id, revision, definition, func() error {
				return runner.Service.RestoreFrozenOptions(ctx, definition, packSync.SyncOptions{WorkspaceRoot: directory, Mode: selected.Mode})
			}); err != nil {
				return err
			}
			capture, err := registry.ProjectCapture(definition, directory, node, selected.Mode)
			if err != nil {
				return fmt.Errorf("environment restored, but recording its revision failed: %w", err)
			}
			capture.ID, capture.ParentIDs, capture.Label = id, []string{revision}, "Restored revision"
			if _, _, err := store.Capture(capture); err != nil {
				return fmt.Errorf("environment restored, but recording its revision failed: %w", err)
			}
			return runner.restartChangedEnvironments(ctx, before)
		},
	}, nil
}

func captureNetworkEnvironment(definition, workspace, mode string) error {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	node, err := os.Hostname()
	if err != nil {
		return err
	}
	_, _, _, err = registry.CaptureProjectIfEnabled(home, definition, workspace, node, mode)
	return err
}
