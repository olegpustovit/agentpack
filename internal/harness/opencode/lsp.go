package opencode

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/tailscale/hujson"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// writeLSP adds the language servers to the lsp map of opencode.json.
// OpenCode spells a server {command: [program, args...], extensions, env,
// initialization}; it has no setting for Claude Code's settings, transport,
// timeouts or restart policy, so those are left out with a warning. A server
// already named in the user's opencode.json is kept as the user wrote it.
func writeLSP(servers map[string]json.RawMessage, ctx base.StageContext) error {
	projection, err := planLSP(servers, ctx)
	if err != nil {
		return err
	}
	return base.WriteLSPProjection(projection)
}

func planLSP(servers map[string]json.RawMessage, ctx base.StageContext) (base.LSPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.LSPProjection{}, err
	}
	if nativeMajor() == 1 || nativeMajor() == 2 {
		return planNativeLSP(servers, ctx, root)
	}
	path := filepath.Join(root, "opencode.json")
	seedPath := path
	if ctx.StagedRoots == nil {
		if home, err := os.UserHomeDir(); err == nil {
			userRoot, err := userConfigRoot(home, nativeMajor())
			if err != nil {
				return base.LSPProjection{}, err
			}
			seedPath = filepath.Join(userRoot, "opencode.json")
		}
	}
	seed := append([]byte(nil), ctx.SeedConfig...)
	if len(seed) == 0 {
		seed, err = base.ReadMCPSeed(seedPath)
		if err != nil {
			return base.LSPProjection{}, err
		}
	}
	if !keepAttribution() {
		seed, err = AttributionData(seed)
		if err != nil {
			return base.LSPProjection{}, err
		}
	}
	data, warnings, omitted, err := MergeLSPData(seed, servers)
	if err != nil {
		return base.LSPProjection{}, err
	}
	return base.LSPProjection{Files: []base.MCPProjectedFile{{Path: path, Contents: data, Scope: "lsp"}}, Warnings: warnings, Omitted: omitted}, nil
}

func MergeLSP(path string, servers map[string]json.RawMessage) error {
	seed, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	data, warnings, _, err := MergeLSPData(seed, servers)
	if err != nil {
		return fmt.Errorf("merge %s: %w", path, err)
	}
	for _, warning := range warnings {
		log.Printf("warning: %s", warning)
	}
	if len(data) == 0 {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

func MergeLSPData(seed []byte, servers map[string]json.RawMessage) ([]byte, []string, []string, error) {
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if len(seed) != 0 {
		standard, err := hujson.Standardize(append(seed, '\n'))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse seed: %w", err)
		}
		if err := json.Unmarshal(standard, &config); err != nil {
			return nil, nil, nil, fmt.Errorf("parse seed: %w", err)
		}
	}
	if enabled, isBool := config["lsp"].(bool); isBool && !enabled {
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, []string{fmt.Sprintf("OpenCode config disables LSP; skipped %d language servers", len(names))}, names, nil
	}
	existing, _ := config["lsp"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var warnings []string
	var omitted []string
	for _, name := range names {
		if _, found := existing[name]; found {
			omitted = append(omitted, name)
			continue
		}
		var server struct {
			Command               string            `json:"command"`
			Args                  []string          `json:"args"`
			ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
			Env                   map[string]string `json:"env"`
			InitializationOptions json.RawMessage   `json:"initializationOptions"`
		}
		if err := json.Unmarshal(servers[name], &server); err != nil {
			return nil, warnings, omitted, fmt.Errorf("language server %q: %w", name, err)
		}
		var all map[string]json.RawMessage
		_ = json.Unmarshal(servers[name], &all)
		var dropped []string
		for key := range all {
			switch key {
			case "command", "args", "extensionToLanguage", "env", "initializationOptions":
			default:
				dropped = append(dropped, key)
			}
		}
		if len(dropped) != 0 {
			sort.Strings(dropped)
			warnings = append(warnings, fmt.Sprintf("OpenCode: language server %q: leaving out %v, which OpenCode has no setting for", name, dropped))
		}
		extensions := make([]string, 0, len(server.ExtensionToLanguage))
		for extension := range server.ExtensionToLanguage {
			extensions = append(extensions, extension)
		}
		sort.Strings(extensions)
		value := map[string]any{"command": append([]string{server.Command}, server.Args...), "extensions": extensions}
		if len(server.Env) != 0 {
			value["env"] = server.Env
		}
		if len(server.InitializationOptions) != 0 {
			value["initialization"] = server.InitializationOptions
		}
		existing[name] = value
	}
	config["lsp"] = existing
	data, err := json.MarshalIndent(config, "", "  ")
	return append(data, '\n'), warnings, omitted, err
}
