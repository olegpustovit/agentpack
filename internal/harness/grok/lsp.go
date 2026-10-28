package grok

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

// writeLSP writes $GROK_HOME/lsp.json, Grok's user-level language server
// file. A plugin's .lsp.json is read only once the plugin is trusted, and a
// project's .grok/lsp.json only in a trusted folder; the home file needs
// neither. Grok gives its model the lsp tool only with [features]
// lsp_tools = true (its README, "LSP"), which Claude Code does unasked, so
// that is set too unless the user's configuration says otherwise.
func writeLSP(servers map[string]json.RawMessage, ctx base.StageContext) error {
	projection, err := planLSP(servers, ctx)
	if err != nil {
		return err
	}
	return base.WriteLSPProjection(projection)
}

func planLSP(servers map[string]json.RawMessage, ctx base.StageContext) (base.LSPProjection, error) {
	home, err := paths.StagingGrokHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return base.LSPProjection{}, err
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return base.LSPProjection{}, err
	}
	data = append(data, '\n')
	configPath := filepath.Join(home, "config.toml")
	seedPath := configPath
	if ctx.StagedRoots == nil {
		if userHome, err := os.UserHomeDir(); err == nil {
			seedPath = filepath.Join(userHome, ".grok", "config.toml")
		}
	}
	seed := append([]byte(nil), ctx.SeedConfig...)
	if len(seed) == 0 {
		seed, err = base.ReadMCPSeed(seedPath)
		if err != nil {
			return base.LSPProjection{}, err
		}
	}
	config, warning, err := EnableLSPToolsData(seed)
	if err != nil {
		return base.LSPProjection{}, err
	}
	projection := base.LSPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(home, "lsp.json"), Contents: data, Scope: "lsp"}, {Path: configPath, Contents: config, Scope: "features"}}}
	if warning {
		projection.Warnings = append(projection.Warnings, "Grok user config already disables LSP tools")
	}
	return projection, nil
}

func enableLSPTools(path string) error {
	var seed []byte
	if data, err := os.ReadFile(path); err == nil {
		seed = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	data, _, err := EnableLSPToolsData(seed)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}

func EnableLSPToolsData(seed []byte) ([]byte, bool, error) {
	root := make(map[string]any)
	if len(seed) != 0 {
		if err := toml.Unmarshal(seed, &root); err != nil {
			return nil, false, err
		}
	}
	features, ok := root["features"].(map[string]any)
	if root["features"] != nil && !ok {
		return nil, false, fmt.Errorf("features must be a TOML table")
	}
	if features == nil {
		features = make(map[string]any)
		root["features"] = features
	}
	if value, set := features["lsp_tools"]; set {
		data, err := toml.Marshal(root)
		return data, value == false, err
	}
	features["lsp_tools"] = true
	data, err := toml.Marshal(root)
	return data, false, err
}
