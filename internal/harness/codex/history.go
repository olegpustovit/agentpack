package codex

import (
	"fmt"
	"os"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

var durableDirectories = []string{"sessions", "archived_sessions"}

const promptHistory = "history.jsonl"

func nativeHome() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".codex"), true
}
func prepareHistory(staged, native string) error {
	if err := os.MkdirAll(native, 0o755); err != nil {
		return err
	}
	for _, name := range durableDirectories {
		if err := base.LinkDurableDirectory(filepath.Join(native, name), filepath.Join(staged, name)); err != nil {
			return err
		}
	}
	if err := base.LinkDurableFile(filepath.Join(native, promptHistory), filepath.Join(staged, promptHistory)); err != nil {
		return err
	}
	return updateConfig(filepath.Join(staged, "config.toml"), func(root map[string]any) {
		if _, exists := root["sqlite_home"]; !exists {
			root["sqlite_home"] = native
		}
	})
}
func verifyHistory(staged, native string) error {
	for _, name := range append(append([]string{}, durableDirectories...), promptHistory) {
		if !base.DurablePathMatches(filepath.Join(staged, name), filepath.Join(native, name)) {
			return fmt.Errorf("codex durable history link %s does not resolve to native state", name)
		}
	}
	root := make(map[string]any)
	data, err := os.ReadFile(filepath.Join(staged, "config.toml"))
	if err != nil {
		return err
	}
	if err := jsonOrToml(data, &root); err != nil {
		return err
	}
	if _, exists := root["sqlite_home"]; !exists {
		return fmt.Errorf("codex staged config is missing durable sqlite_home")
	}
	return nil
}
