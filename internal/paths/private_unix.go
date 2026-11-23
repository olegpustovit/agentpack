//go:build !windows

package paths

import (
	"fmt"
	"os"
	"syscall"
)

// EnsurePrivateDirectory protects both newly created and existing directories.
func EnsurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func checkStagingOwnership(path string, info os.FileInfo, _ bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("staging root must be owned by the current user and not writable by others: %s", path)
	}
	// Earlier releases created the root 0755; nobody else could write into it, so tightening it is safe.
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(path, 0o700)
	}
	return nil
}
