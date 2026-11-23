package grok

import (
	"fmt"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/pelletier/go-toml/v2"
)

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
		// Grok's [mcp_servers] documents headers for a remote server; its
		// OAuth flow takes no client settings, and a stdio server no cwd.
		var missing []string
		if server.IsRemote() {
			value["url"] = *server.URL
			if len(server.Headers) != 0 {
				value["headers"] = server.Headers
			}
			if server.OAuth != nil {
				missing = append(missing, "OAuth client settings")
			}
		} else {
			if server.Cwd != nil {
				missing = append(missing, "a working directory (cwd)")
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
			warnings = append(warnings, mcp.NotCarried("Grok", name, missing...))
		}
		servers[name] = value
	}
	data, err := toml.Marshal(root)
	if err != nil {
		return nil, warnings, nil, fmt.Errorf("encode MCP config: %w", err)
	}
	return data, warnings, shadowed, nil
}
