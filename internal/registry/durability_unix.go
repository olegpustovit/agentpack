//go:build !windows

package registry

import (
	"os"
	"path/filepath"
)

func publishExclusive(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func renameDurable(source, target string, replace bool) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func removeFileDurable(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
