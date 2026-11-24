package opencode

import (
	"encoding/json"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/tailscale/hujson"
)

func MergeMCPData(data []byte, entries mcp.Entries) ([]byte, []string, []string, error) {
	root := map[string]any{"$schema": "https://opencode.ai/config.json"}
	if len(data) != 0 {
		standard, err := hujson.Standardize(append(data, '\n'))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse MCP seed: %w", err)
		}
		if err := json.Unmarshal(standard, &root); err != nil {
			return nil, nil, nil, fmt.Errorf("parse MCP seed: %w", err)
		}
	}
	servers, ok := root["mcp"].(map[string]any)
	if root["mcp"] != nil && !ok {
		return nil, nil, nil, fmt.Errorf("MCP seed: mcp must be a JSON object")
	}
	if servers == nil {
		servers = make(map[string]any)
		root["mcp"] = servers
	}
	warnings, shadowed := mcp.MergeJSONServers(servers, entries, "OpenCode", renderMCPServer)
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, warnings, nil, fmt.Errorf("encode MCP config: %w", err)
	}
	return data, warnings, shadowed, nil
}

func renderMCPServer(server mcp.Server) (map[string]any, []string) {
	value := map[string]any{"enabled": server.Disabled == nil || !*server.Disabled}
	if !server.IsRemote() {
		if server.Cwd != nil {
			value["cwd"] = *server.Cwd
		}
		value["type"] = "local"
		command := append([]string(nil), server.Args...)
		if server.Command != nil {
			command = append([]string{*server.Command}, command...)
		}
		value["command"] = command
		if len(server.Env) != 0 {
			value["environment"] = server.Env
		}
		return value, nil
	}
	value["type"], value["url"] = "remote", *server.URL
	if len(server.Headers) != 0 {
		value["headers"] = server.Headers
	}
	var missing []string
	if oauth := server.OAuth; oauth != nil {
		if client := oauth.JSONClient(); len(client) != 0 {
			value["oauth"] = client
		}
		if oauth.CallbackPort != 0 {
			missing = append(missing, "an OAuth callback port")
		}
		if oauth.MetadataURL != "" {
			missing = append(missing, "an OAuth metadata URL")
		}
	}
	return value, missing
}
