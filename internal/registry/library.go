package registry

import (
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
	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// RecentProject is a folder this node launched an agent in.
type RecentProject struct {
	Directory, DefinitionRoot, Mode string
}

// Bootstrapper captures recent projects into the Library and imports cached
// packages whenever the cache index changed. Recent and Definition come from
// the caller, because the Library cannot depend on the gateway or the CLI.
type Bootstrapper struct {
	Store  *Store
	Home   string
	Node   string
	Recent func() ([]RecentProject, error)
	// Definition resolves a workspace (and recorded definition root) to its definition root.
	Definition func(workspace, definition string) (string, error)

	mu             sync.Mutex
	lastCacheIndex string
}

func (b *Bootstrapper) Run() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.captureRecent(); err != nil {
		return err
	}
	stamp := ""
	for _, name := range []string{"index.db", "index.db-wal"} {
		if info, err := os.Stat(filepath.Join(b.Home, "cache", name)); err == nil {
			stamp += fmt.Sprintf("%s:%d:%d;", name, info.ModTime().UnixNano(), info.Size())
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if stamp != b.lastCacheIndex {
		if err := ImportCachedPackages(b.Store); err != nil {
			return err
		}
		b.lastCacheIndex = stamp
	}
	return nil
}

func (b *Bootstrapper) captureRecent() error {
	store, node := b.Store, b.Node
	recent, err := b.Recent()
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
			if key := projectKey(revision.Project); key != "" {
				knownProjects[key] = true
			}
		}
	}
	seenRoots := map[string]bool{}
	for _, entry := range recent {
		if !filepath.IsAbs(entry.Directory) || environment.ProjectRequiresUserAccess(entry.Directory, entry.DefinitionRoot) {
			continue
		}
		if info, err := os.Stat(entry.Directory); err != nil || !info.IsDir() {
			continue
		}
		definition, err := b.Definition(entry.Directory, entry.DefinitionRoot)
		if err != nil {
			continue
		}
		if seenRoots[definition] {
			continue
		}
		seenRoots[definition] = true
		id := projectID(node, definition)
		if knownIDs[id] || knownProjects[projectKey(entry.Directory)] || knownProjects[projectKey(definition)] {
			continue
		}
		deleted, err := store.AgentpackDeleted(id)
		if err != nil {
			return fmt.Errorf("check deleted project in Library: %w", err)
		}
		if deleted {
			continue
		}
		if _, err := manifest.Load(definition); err != nil {
			continue
		}
		locked, err := lockfile.LoadFromPath(paths.LockPath(definition))
		if err != nil || cache.CheckLockEntries(locked) != nil {
			continue
		}
		capture, err := ProjectCapture(definition, entry.Directory, node, entry.Mode)
		if err != nil {
			continue
		}
		capture.ID = id
		if _, _, err := store.Capture(capture); err != nil {
			return fmt.Errorf("capture existing project in Library: %w", err)
		}
		knownIDs[id] = true
		knownProjects[projectKey(entry.Directory)] = true
		knownProjects[projectKey(definition)] = true
	}
	return nil
}

func projectKey(directory string) string {
	if directory == "" {
		return ""
	}
	if !paths.RequiresUserAccess(directory) {
		if canonical, err := filepath.EvalSymlinks(directory); err == nil {
			return filepath.Clean(canonical)
		}
	}
	if absolute, err := filepath.Abs(directory); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(directory)
}

func projectID(node, definition string) string {
	sum := sha256.Sum256([]byte("agentpack-library-project-v1\x00" + node + "\x00" + projectKey(definition)))
	return hex.EncodeToString(sum[:16])
}

// ImportCachedPackages adds every intact GitHub package in the user cache to the Library.
func ImportCachedPackages(store *Store) error {
	keys, err := cache.ListKeys()
	if err != nil {
		return fmt.Errorf("list cached packages for Library: %w", err)
	}
	packages := []Package{}
	for _, key := range keys {
		if key == "" || !filepath.IsLocal(key) || filepath.Base(key) != key || strings.ContainsAny(key, `/\`) {
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
		packages = append(packages, Package{
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

// DraftStateOf reports a draft's pins against the project at definition and
// which of the packages it needs this machine still has to fetch.
func (s *Store) DraftStateOf(draft Draft, definition string) (DraftState, error) {
	state := DraftState{Pins: map[string]PackagePin{}}
	snapshot, err := readReviewSnapshot(definition)
	if err != nil {
		return state, err
	}
	if !snapshot.hadLock {
		return state, fmt.Errorf("project lockfile is missing")
	}
	locked, err := decodeLock(snapshot.lock)
	if err != nil {
		return state, err
	}
	if err := cache.CheckLockEntries(locked); err != nil {
		return state, err
	}
	for _, pkg := range locked.Packages {
		state.Pins[pkg.Module] = PackagePin{Commit: pkg.Commit, ContentHash: pkg.ContentHash, CacheKey: pkg.CacheKey}
	}
	packages, err := s.ListPackages()
	if err != nil {
		return state, err
	}
	var needed []Package
	if draft.PackID != "" {
		revision, diff, err := s.Preview(draft.PackID, draft.Revision, definition)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
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
		diff := DiffResult{Add: []string{}, Update: []string{}, Remove: []string{}, PreviewDigest: snapshot.digest(definition, "install:"+draft.Item)}
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

// ProjectCapture reads a project's manifest and lock as a Library capture.
func ProjectCapture(definition, project, node, mode string) (CaptureInput, error) {
	manifest, err := os.ReadFile(paths.ManifestPath(definition))
	if err != nil {
		return CaptureInput{}, err
	}
	locked, err := os.ReadFile(paths.LockPath(definition))
	if err != nil {
		return CaptureInput{}, err
	}
	return CaptureInput{Manifest: manifest, Lock: locked, SourceNode: node, Project: project, Mode: mode}, nil
}
