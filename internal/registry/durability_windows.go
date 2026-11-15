//go:build windows

package registry

import (
	"github.com/olegpustovit/agentpack/internal/paths"
	"os"
	"path/filepath"
)

func removeFileDurable(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".removed-")
	if err != nil {
		return err
	}
	marker := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(marker)
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	if err := paths.RenameDurable(path, marker, false); err != nil {
		return err
	}
	// A crash can leave the hidden marker, but the original journal path is durably gone.
	return os.Remove(marker)
}
