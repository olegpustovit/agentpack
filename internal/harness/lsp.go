package harness

import (
	"os"
	"path/filepath"
)

func WriteLSPProjection(projection LSPProjection) error {
	for _, file := range projection.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file.Path, file.Contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}
