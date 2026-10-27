//go:build linux || darwin

package network

import (
	"fmt"
	"os"
	"syscall"
)

func FolderIdentity(directory string) (string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", directory)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
