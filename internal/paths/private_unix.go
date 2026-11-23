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
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("staging root must be owned by the current user with mode 0700: %s", path)
	}
	return nil
}
