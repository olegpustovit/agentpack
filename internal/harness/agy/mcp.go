package agy

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

// WriteMCP writes the servers as Antigravity's mcp_config.json. A remote
// server is named by serverUrl and keeps its headers and its OAuth client
// (oauth.clientId, oauth.clientSecret); a stdio server keeps its cwd.
// Antigravity's OAuth callback is fixed and it documents no scopes or
// metadata URL, so those are left out with a warning.
func WriteMCP(path string, entries mcp.Entries) error {
	data, warnings, err := RenderMCP(entries)
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		log.Printf("warning: %s", warning)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

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
