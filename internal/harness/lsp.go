package harness

import (
	"encoding/json"
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

// WriteLSPFile writes language servers as a .lsp.json file, the format that
// Claude Code reads at a plugin root and Grok reads as $GROK_HOME/lsp.json.
func WriteLSPFile(path string, servers map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
