package agy

import (
	"encoding/json"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	servers := make(map[string]map[string]any, len(entries))
	var warnings []string
	for _, name := range entries.Names() {
		server, value := entries[name].Server, make(map[string]any)
		var missing []string
		if server.IsRemote() {
			value["serverUrl"] = *server.URL
			if len(server.Headers) != 0 {
				value["headers"] = server.Headers
			}
			if oauth := server.OAuth; oauth != nil {
				if oauth.ClientID != "" {
					client := map[string]any{"clientId": oauth.ClientID}
					if oauth.ClientSecret != "" {
						client["clientSecret"] = oauth.ClientSecret
					}
					value["oauth"] = client
				}
				if len(oauth.Scopes) != 0 {
					missing = append(missing, "OAuth scopes")
				}
				if oauth.CallbackPort != 0 {
					missing = append(missing, "an OAuth callback port")
				}
				if oauth.MetadataURL != "" {
					missing = append(missing, "an OAuth metadata URL")
				}
			}
		} else {
			if server.Command != nil {
				value["command"] = *server.Command
			}
			if len(server.Args) != 0 {
				value["args"] = server.Args
			}
			if len(server.Env) != 0 {
				value["env"] = server.Env
			}
			if server.Cwd != nil {
				value["cwd"] = *server.Cwd
			}
		}
		if server.Disabled != nil {
			value["disabled"] = *server.Disabled
		}
		if len(missing) != 0 {
			warnings = append(warnings, mcp.NotCarried("Antigravity", name, missing...))
		}
		servers[name] = value
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, warnings, fmt.Errorf("encode MCP servers: %w", err)
	}
	return data, warnings, nil
}
