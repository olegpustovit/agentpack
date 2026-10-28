package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func (runner Runner) networkLibrary(home string) (network.LibraryOptions, error) {
	store, err := registry.New(home)
	if err != nil {
		return network.LibraryOptions{}, err
	}
	node, err := os.Hostname()
	if err != nil {
		return network.LibraryOptions{}, err
	}
	return network.LibraryOptions{
		Store: store,
		DraftState: func(ctx context.Context, draft registry.Draft) (registry.DraftState, error) {
			return registryDraftState(store, draft)
		},
		Save: func(ctx context.Context, input network.SaveAgentpackInput) (network.SavedAgentpack, error) {
			packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: input.Directory}}, true)
			if err != nil {
				return network.SavedAgentpack{}, err
			}
			mutation, err := acquireDefinitionMutation(ctx, packContext.DefinitionRoot)
			if err != nil {
				return network.SavedAgentpack{}, err
			}
			defer mutation.Unlock()
			capture, err := registryCapture(packContext.DefinitionRoot, input.Directory, node, input.Mode)
			if err != nil {
				return network.SavedAgentpack{}, err
			}
			capture.ID, capture.Name, capture.Label = input.AgentpackID, input.Name, input.Label
			pack, revision, err := store.Capture(capture)
			if err == nil {
				err = store.SetCapture(packContext.DefinitionRoot, pack.ID, input.AutoCapture)
			}
			return network.SavedAgentpack{Agentpack: pack, Revision: revision}, err
		},
		Preview: func(ctx context.Context, id, revision, directory string) (registry.DiffResult, error) {
			packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
			if err != nil {
				return registry.DiffResult{}, err
			}
			_, diff, err := store.Preview(id, revision, packContext.DefinitionRoot)
			return diff, err
		},
		Restore: func(ctx context.Context, id, revision, directory string, restart bool) error {
			packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: directory}}, true)
			if err != nil {
				return err
			}
			mutation, err := acquireDefinitionMutation(ctx, packContext.DefinitionRoot)
			if err != nil {
				return err
			}
			defer mutation.Unlock()
			selected, _, err := store.Preview(id, revision, packContext.DefinitionRoot)
			if err != nil {
				return err
			}
			var before []runningEnvironment
			if restart {
				before, err = snapshotRunningEnvironments(ctx, packContext.DefinitionRoot)
				if err != nil {
					return err
				}
			}
			if err := store.Restore(id, revision, packContext.DefinitionRoot, func() error {
				return runner.Service.RestoreFrozenOptions(ctx, packContext.DefinitionRoot, packSync.SyncOptions{WorkspaceRoot: directory, Mode: selected.Mode})
			}); err != nil {
				return err
			}
			capture, err := registryCapture(packContext.DefinitionRoot, directory, node, selected.Mode)
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

func registryDraftState(store *registry.Store, draft registry.Draft) (registry.DraftState, error) {
	state := registry.DraftState{Pins: map[string]registry.PackagePin{}}
	packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: draft.Directory}}, true)
	if err != nil {
		return state, err
	}
	locked, err := lockfile.Load(packContext.DefinitionRoot)
	if err != nil {
		return state, err
	}
	if err := cache.CheckLockEntries(locked); err != nil {
		return state, err
	}
	for _, pkg := range locked.Packages {
		state.Pins[pkg.Module] = registry.PackagePin{Commit: pkg.Commit, ContentHash: pkg.ContentHash, CacheKey: pkg.CacheKey}
	}
	packages, err := store.ListPackages()
	if err != nil {
		return state, err
	}
	var needed []registry.Package
	if draft.PackID != "" {
		revision, diff, err := store.Preview(draft.PackID, draft.Revision, packContext.DefinitionRoot)
		if err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				state.MissingObjects, state.NeededHolder = []string{draft.Revision}, draft.NeededHolder
				return state, nil
			}
			return state, fmt.Errorf("exact revision is unavailable on this machine; sync its library first: %w", err)
		}
		state.CurrentDiff = &diff
		for _, pkg := range packages {
			if slices.Contains(revision.PackageIDs, pkg.ID) {
				needed = append(needed, pkg)
			}
		}
	} else {
		index := strings.LastIndex(draft.Item, "@")
		if index < 1 || index == len(draft.Item)-1 {
			return state, fmt.Errorf("draft package requires an exact module@commit")
		}
		module, commit := draft.Item[:index], draft.Item[index+1:]
		for _, pkg := range packages {
			if pkg.Module == module && pkg.Commit == commit {
				needed = append(needed, pkg)
				break
			}
		}
		if len(needed) == 0 {
			bytes, err := hex.DecodeString(commit)
			if err != nil || len(bytes) != 20 || !strings.HasPrefix(module, "github.com/") {
				state.MissingObjects, state.NeededHolder = []string{draft.Item}, draft.NeededHolder
				return state, nil
			}
		}
		diff := registry.DiffResult{Add: []string{}, Update: []string{}, Remove: []string{}}
		if current, exists := state.Pins[module]; !exists {
			diff.Add = append(diff.Add, module)
		} else if current.Commit != commit {
			diff.Update = append(diff.Update, module)
		}
		state.CurrentDiff = &diff
	}
	for _, pkg := range needed {
		// GitHub commits can be fetched exactly on confirmation; local-only bytes need their holder.
		if pkg.Owner != "" && pkg.Repo != "" && strings.HasPrefix(pkg.URL, "https://github.com/") {
			continue
		}
		pin := lockfile.Package{Module: pkg.Module, Commit: pkg.Commit, CacheKey: pkg.CacheKey, ContentHash: pkg.ContentHash, Kind: lockfile.PackageKind(pkg.Kind)}
		directory, err := cache.EntryDir(pkg.CacheKey)
		if err != nil {
			return state, err
		}
		if !cache.IsPackageRoot(directory) {
			state.MissingObjects = append(state.MissingObjects, pkg.ID)
			state.NeededHolder = pkg.SourceNode
			if state.NeededHolder == "" {
				state.NeededHolder = draft.NeededHolder
			}
			continue
		}
		if err := cache.VerifyPackage(pin); err != nil {
			return state, err
		}
	}
	return state, nil
}

func registryCapture(definition, project, node, mode string) (registry.CaptureInput, error) {
	manifest, err := os.ReadFile(paths.ManifestPath(definition))
	if err != nil {
		return registry.CaptureInput{}, err
	}
	locked, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		return registry.CaptureInput{}, err
	}
	return registry.CaptureInput{Manifest: manifest, Lock: locked, SourceNode: node, Project: project, Mode: mode}, nil
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
