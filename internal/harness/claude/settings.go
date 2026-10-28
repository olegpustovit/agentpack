package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func KeepAttribution() bool {
	switch strings.ToLower(os.Getenv("AGENTPACK_KEEP_ATTRIBUTION")) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// SettingsPath is mode-scoped; inline launch settings do not alter the keychain namespace.
func SettingsPath(ctx base.StageContext) (string, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "agentpack-settings.json"), nil
}

func MaterializeSettings(ctx base.StageContext) error {
	data, err := SettingsContents(nil, KeepAttribution())
	if err != nil {
		return err
	}
	return WriteSettingsContents(ctx, data)
}

// SettingsContents is the ordered agentpack-owned Claude settings overlay.
func SettingsContents(names []string, keepAttribution bool) ([]byte, error) {
	settings := map[string]any{}
	if !keepAttribution {
		settings["includeCoAuthoredBy"] = false
		settings["attribution"] = map[string]any{"commit": "", "pr": ""}
	}
	if names != nil {
		settings["enabledMcpjsonServers"] = names
	}
	return json.MarshalIndent(settings, "", "  ")
}

func WriteSettingsContents(ctx base.StageContext, data []byte) error {
	path, err := SettingsPath(ctx)
	if err != nil {
		return err
	}
	return writeSettingsBytes(path, data)
}

func SetMCPAllowlist(ctx base.StageContext, names []string) error {
	path, err := SettingsPath(ctx)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	settings := make(map[string]any)
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if names == nil {
		names = []string{}
	}
	settings["enabledMcpjsonServers"] = names
	return writeSettings(path, settings)
}

func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeSettingsBytes(path, data)
}

func writeSettingsBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
