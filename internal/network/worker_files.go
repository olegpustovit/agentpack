package network

import (
	"errors"
	"os"
	"path/filepath"
)

func removeWorkerEndpoint(directory string) error {
	var result error
	for _, name := range []string{"worker.json", "worker.tmp", ChatToolsTokenFile} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	// Images and support files outlive their process; an empty directory can disappear.
	_ = os.Remove(directory)
	return result
}
