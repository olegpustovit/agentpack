package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// Install is the one way bytes enter the cache. fill writes the package into
// an empty temporary sibling of its slot; that tree is normalized, must be a
// package, is digested and, when pkg has a content hash, must match it. Only
// then does it replace the slot: the old entry is moved aside, the new one
// renamed in, the old one removed. A slot that already holds the same tree
// is kept as it is, with its files and verification stamp. Nothing a failed
// fill wrote is ever visible in the slot.
func Install(pkg lockfile.Package, fill func(string) error) (digest string, replaced bool, err error) {
	out, err := EntryDir(pkg.CacheKey)
	if err != nil {
		return "", false, err
	}
	temporary, digest, err := stage(pkg, fill)
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(temporary)
	replaced, err = swap(temporary, out, digest)
	return digest, replaced, err
}

// RefetchPackage builds the package again from its pinned source and installs
// it over the cache entry when it differs from what is cached. A fetched tree
// that contradicts the lock entry's content hash leaves the cache untouched.
func RefetchPackage(pkg lockfile.Package, restoreRemote RemoteRestoreFunc) (digest string, replaced bool, err error) {
	fill, err := packageSource(pkg, restoreRemote)
	if err != nil {
		return "", false, err
	}
	if fill == nil {
		return "", false, fmt.Errorf("cannot re-fetch %s: its source is unavailable here", pkg.Module)
	}
	return Install(pkg, fill)
}

// packageSource returns the function that writes the package tree into a
// directory, or nil when the package is local and its source is missing.
func packageSource(pkg lockfile.Package, restoreRemote RemoteRestoreFunc) (func(string) error, error) {
	local, err := localSourceDir(pkg.URL)
	if err != nil {
		return nil, err
	}
	if local != "" {
		if info, statErr := os.Stat(local); statErr == nil && info.IsDir() {
			return func(destination string) error {
				_, err := hashAndCopySourceTree(local, destination)
				return err
			}, nil
		}
	}
	if isLocalPackage(pkg) {
		return nil, nil
	}
	if restoreRemote == nil {
		return nil, fmt.Errorf("cache entry %s is missing and no remote restorer is configured", pkg.CacheKey)
	}
	return func(destination string) error { return restoreRemote(pkg, destination) }, nil
}

// stage fills a temporary sibling of the cache entries, normalizes it, and
// checks it against the lock before anything can read it as cache content.
func stage(pkg lockfile.Package, fill func(string) error) (temporary, digest string, err error) {
	cacheRoot, err := paths.CacheDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", "", fmt.Errorf("create cache directory %s: %w", cacheRoot, err)
	}
	directory, err := os.MkdirTemp(cacheRoot, ".tmp-fetch-")
	if err != nil {
		return "", "", fmt.Errorf("create cache temporary directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	if err = fill(directory); err != nil {
		return "", "", err
	}
	if err = os.Chmod(directory, 0o755); err != nil {
		return "", "", err
	}
	if err = NormalizePluginLayout(directory); err != nil {
		return "", "", err
	}
	if !HasPluginManifest(directory) && !regularFile(filepath.Join(directory, "SKILL.md")) {
		return "", "", fmt.Errorf("invalid cache layout: %s is not a package: it has no SKILL.md, plugin manifest (.claude-plugin, .cursor-plugin, .codex-plugin or .omp-plugin), agentpack.toml or marketplace manifest", describe(pkg))
	}
	if digest, err = TreeDigest(directory); err != nil {
		return "", "", err
	}
	if pkg.ContentHash != "" && digest != pkg.ContentHash {
		return "", "", &IntegrityError{Package: pkg, Expected: pkg.ContentHash, Actual: digest, Fetched: true}
	}
	return directory, digest, nil
}

func describe(pkg lockfile.Package) string {
	for _, name := range []string{pkg.Module, pkg.URL, pkg.CacheKey} {
		if name != "" {
			return name
		}
	}
	return "the fetched tree"
}

// swap installs the staged tree at out unless out already holds the same
// tree. Another process installing the same key at the same time is success
// when the tree it left is the same.
func swap(temporary, out, digest string) (bool, error) {
	if current, err := TreeDigest(out); err == nil && current == digest {
		return false, nil
	}
	aside, err := os.MkdirTemp(filepath.Dir(out), ".tmp-old-")
	if err != nil {
		return false, fmt.Errorf("create cache temporary directory: %w", err)
	}
	defer os.RemoveAll(aside)
	old := filepath.Join(aside, "entry")
	if err := os.Rename(out, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("move cache entry %s aside: %w", out, err)
	}
	if err := os.Rename(temporary, out); err != nil {
		if current, digestErr := TreeDigest(out); digestErr == nil && current == digest {
			return true, nil
		}
		// Put the old entry back when nothing took its place.
		_ = os.Rename(old, out)
		return false, fmt.Errorf("install cache entry %s: %w", out, err)
	}
	return true, nil
}
