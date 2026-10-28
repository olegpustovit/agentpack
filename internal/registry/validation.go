package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type safeParseError struct{ cause error }

func (err safeParseError) Error() string { return "invalid snapshot document" }
func (err safeParseError) Unwrap() error { return err.cause }

func validateSnapshot(manifestData, lockData []byte) (lockfile.PackLock, error) {
	directory, err := os.MkdirTemp("", "agentpack-registry-validate-")
	if err != nil {
		return lockfile.PackLock{}, fmt.Errorf("create snapshot validation directory: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := os.WriteFile(filepath.Join(directory, paths.ManifestName), manifestData, 0o600); err != nil {
		return lockfile.PackLock{}, fmt.Errorf("write temporary manifest: %w", err)
	}
	if err := os.WriteFile(paths.LockPath(directory), lockData, 0o600); err != nil {
		return lockfile.PackLock{}, fmt.Errorf("write temporary lock: %w", err)
	}
	if _, err := manifest.Load(directory); err != nil {
		return lockfile.PackLock{}, fmt.Errorf("validate normalized manifest: %w", safeParseError{cause: err})
	}
	lock, err := lockfile.Load(directory)
	if err != nil {
		return lockfile.PackLock{}, fmt.Errorf("validate normalized lock: %w", safeParseError{cause: err})
	}
	for _, pkg := range lock.Packages {
		if !validCacheKey(pkg.CacheKey) {
			return lockfile.PackLock{}, fmt.Errorf("invalid package cache key")
		}
	}
	return lock, nil
}

func validCacheKey(key string) bool {
	return key != "" && filepath.IsLocal(key) && filepath.Base(key) == key && !strings.ContainsAny(key, `/\\`)
}
