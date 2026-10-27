//go:build !windows

package network

import "os"

// EnsurePrivateDirectory protects both newly created and existing directories.
func EnsurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}
