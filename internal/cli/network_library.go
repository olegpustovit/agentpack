package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
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
	var bootstrapMu sync.Mutex
	lastCacheIndex := ""
	return network.LibraryOptions{
		Store: store,
		Bootstrap: func() error {
			bootstrapMu.Lock()
			defer bootstrapMu.Unlock()
			if err := bootstrapLibraryProjects(home, store, node); err != nil {
				return err
			}
			stamp := ""
			if info, err := os.Stat(filepath.Join(home, "cache", "db.reddb")); err == nil {
				stamp = fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
			} else if !os.IsNotExist(err) {
				return err
			}
			if stamp != lastCacheIndex {
				if err := importCachedPackages(store); err != nil {
					return err
				}
				lastCacheIndex = stamp
			}
			return nil
		},
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

func bootstrapLibraryProjects(home string, store *registry.Store, node string) error {
	recent, err := network.Recent(home)
	if err != nil {
		return fmt.Errorf("read recent projects for Library: %w", err)
	}
	packs, err := store.ListAgentpacks()
	if err != nil {
		return err
	}
	knownIDs, knownProjects := map[string]bool{}, map[string]bool{}
	for _, pack := range packs {
		knownIDs[pack.ID] = true
		_, revisions, err := store.Get(pack.ID)
		if err != nil {
			return fmt.Errorf("read existing agentpack project associations: %w", err)
		}
		for _, revision := range revisions {
			if revision.SourceNode != "" && revision.SourceNode != node {
				continue
			}
			if key := libraryProjectKey(revision.Project); key != "" {
				knownProjects[key] = true
			}
		}
	}
	seenRoots := map[string]bool{}
	for _, entry := range recent {
		if !filepath.IsAbs(entry.Directory) {
			continue
		}
		if info, err := os.Stat(entry.Directory); err != nil || !info.IsDir() {
			continue
		}
		packContext, err := resolveInvocationContext(Invocation{Global: Global{WorkspaceRoot: entry.Directory, DefinitionRoot: entry.DefinitionRoot}}, true)
		if err != nil {
			continue
		}
		definition := packContext.DefinitionRoot
		if seenRoots[definition] {
			continue
		}
		seenRoots[definition] = true
		if knownIDs[libraryProjectID(node, definition)] || knownProjects[libraryProjectKey(entry.Directory)] || knownProjects[libraryProjectKey(definition)] {
			continue
		}
		deleted, err := store.AgentpackDeleted(libraryProjectID(node, definition))
		if err != nil {
			return fmt.Errorf("check deleted project in Library: %w", err)
		}
		if deleted {
			continue
		}
		if _, err := manifest.Load(definition); err != nil {
			continue
		}
		if _, err := lockfile.Load(definition); err != nil {
			continue
		}
		capture, err := registryCapture(definition, entry.Directory, node, entry.Mode)
		if err != nil {
			continue
		}
		capture.ID = libraryProjectID(node, definition)
		if _, _, err := store.Capture(capture); err != nil {
			return fmt.Errorf("capture existing project in Library: %w", err)
		}
		knownIDs[capture.ID] = true
		knownProjects[libraryProjectKey(entry.Directory)] = true
		knownProjects[libraryProjectKey(definition)] = true
	}
	return nil
}

func libraryProjectKey(directory string) string {
	if directory == "" {
		return ""
	}
	if canonical, err := filepath.EvalSymlinks(directory); err == nil {
		return filepath.Clean(canonical)
	}
	if absolute, err := filepath.Abs(directory); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(directory)
}

func libraryProjectID(node, definition string) string {
	sum := sha256.Sum256([]byte("agentpack-library-project-v1\x00" + node + "\x00" + libraryProjectKey(definition)))
	return hex.EncodeToString(sum[:16])
}

func importCachedPackages(store *registry.Store) error {
	keys, err := cache.ListKeys()
	if err != nil {
		return fmt.Errorf("list cached packages for Library: %w", err)
	}
	packages := []registry.Package{}
	for _, key := range keys {
		if key == "" || !filepath.IsLocal(key) || filepath.Base(key) != key || strings.ContainsAny(key, `/\\`) {
			continue
		}
		record, ok, err := cache.GetEntry(key)
		if err != nil {
			continue
		}
		if !ok || record.Owner == "" || record.Repo == "" || record.Owner == "path" || record.Owner == "local" || record.Commit == "" ||
			(record.Kind != lockfile.PackageSkill && record.Kind != lockfile.PackagePlugin) {
			continue
		}
		module, ok := cachedModule(record.Owner, record.Repo, record.Path)
		if !ok {
			continue
		}
		root, err := cache.EntryDir(key)
		if err != nil {
			continue
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !cache.IsPackageRoot(root) {
			continue
		}
		name := filepath.Base(filepath.FromSlash(record.Path))
		if record.Path == "" {
			name = record.Repo
		}
		packages = append(packages, registry.Package{
			Module: module, Commit: record.Commit, Kind: string(record.Kind),
			URL: record.SourceURL, Owner: record.Owner, Repo: record.Repo, Path: record.Path,
			CacheKey: key, Name: name,
		})
	}
	if err := store.ImportPackages(packages); err != nil {
		return fmt.Errorf("add cached packages to Library: %w", err)
	}
	return nil
}

func cachedModule(owner, repo, inRepoPath string) (string, bool) {
	validPart := func(value string) bool {
		return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\")
	}
	if !validPart(owner) || !validPart(repo) {
		return "", false
	}
	module := "github.com/" + strings.ToLower(owner) + "/" + strings.ToLower(repo)
	if inRepoPath == "" {
		return module, true
	}
	inRepoPath = strings.ReplaceAll(inRepoPath, "\\", "/")
	for _, part := range strings.Split(inRepoPath, "/") {
		if !validPart(part) {
			return "", false
		}
	}
	return cache.DependencyKey("", owner, repo, inRepoPath), true
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
