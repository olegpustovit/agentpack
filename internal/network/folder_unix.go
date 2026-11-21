//go:build linux || darwin

package network

import (
	"fmt"
	"os"
	"syscall"
)

// FolderIdentity is the directory's inode. The device number is left out: macOS assigns it at
// mount time, so it changes across reboots and would make every recent folder look replaced.
func FolderIdentity(directory string) (string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", directory)
	}
	return fmt.Sprintf("%x", info.Sys().(*syscall.Stat_t).Ino), nil
}
