package registry

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
)

type safeParseError struct{ cause error }

func (err safeParseError) Error() string { return "invalid snapshot document" }
func (err safeParseError) Unwrap() error { return err.cause }

func validateSnapshot(manifestData, lockData []byte) (lockfile.PackLock, error) {
	if _, err := manifest.Parse(manifestData); err != nil {
		return lockfile.PackLock{}, fmt.Errorf("validate normalized manifest: %w", safeParseError{cause: err})
	}
	lock, err := lockfile.Parse(lockData)
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
