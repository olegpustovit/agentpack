//go:build !windows

package paths

import (
	"errors"
	"os"
	"path/filepath"
)

func RenameDurable(source, target string, replace bool) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
