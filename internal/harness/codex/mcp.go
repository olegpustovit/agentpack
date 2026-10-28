package codex

import (
	"fmt"
	"log"
	"os"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/pelletier/go-toml/v2"
)

func MergeMCP(path string, entries mcp.Entries) error { return mergeMCP(path, entries) }

func mergeMCP(path string, entries mcp.Entries) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	output, warnings, _, err := MergeMCPData(data, entries)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		log.Printf("warning: %s", warning)
	}
	if err := os.WriteFile(path, output, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func MergeMCPData(data []byte, entries mcp.Entries) ([]byte, []string, []string, error) {
	root := make(map[string]any)
	if len(data) != 0 {
		if err := toml.Unmarshal(data, &root); err != nil {
			return nil, nil, nil, fmt.Errorf("parse MCP seed: %w", err)
		}
	}
	servers, ok := root["mcp_servers"].(map[string]any)
	if root["mcp_servers"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("MCP seed: mcp_servers must be a TOML table")
	}
	if servers == nil {
		servers = make(map[string]any)
		root["mcp_servers"] = servers
	}
	var warnings []string
	var shadowed []string
	for _, name := range entries.Names() {
		if _, exists := servers[name]; exists {
			shadowed = append(shadowed, name)
			continue
		}
		server := entries[name].Server
		value := make(map[string]any)
		var missing []string
		if server.IsRemote() {
			value["url"] = *server.URL
			if len(server.Headers) != 0 {
				value["http_headers"] = server.Headers
			}
			// Codex keeps the client under [mcp_servers.<name>.oauth] and the
			// scopes next to it; it has no place for a client secret.
			if oauth := server.OAuth; oauth != nil {
				client := make(map[string]any)
				if oauth.ClientID != "" {
					client["client_id"] = oauth.ClientID
				}
				if oauth.CallbackPort != 0 {
					client["callback_port"] = oauth.CallbackPort
				}
				if len(client) != 0 {
					value["oauth"] = client
				}
				if len(oauth.Scopes) != 0 {
					value["scopes"] = oauth.Scopes
				}
				if oauth.ClientSecret != "" {
					missing = append(missing, "an OAuth client secret")
				}
				if oauth.MetadataURL != "" {
					missing = append(missing, "an OAuth metadata URL")
				}
			}
		} else {
			if server.Cwd != nil {
				value["cwd"] = *server.Cwd
			}
			if server.Command != nil {
				value["command"] = *server.Command
			}
			if len(server.Args) != 0 {
				value["args"] = server.Args
			}
			if len(server.Env) != 0 {
				value["env"] = server.Env
			}
		}
		if server.Disabled != nil && *server.Disabled {
			value["enabled"] = false
		}
		if len(missing) != 0 {
			warnings = append(warnings, mcp.NotCarried("Codex", name, missing...))
		}
		servers[name] = value
	}
	data, err := toml.Marshal(root)
	if err != nil {
		return nil, warnings, nil, fmt.Errorf("encode MCP config: %w", err)
	}
	return data, warnings, shadowed, nil
}
