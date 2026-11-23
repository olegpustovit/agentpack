package cursor

import (
	"encoding/json"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	servers := make(map[string]map[string]any, len(entries))
	var warnings []string
	for _, name := range entries.Names() {
		server := entries[name].Server
		object := server.JSONObject()
		var missing []string
		if server.Cwd != nil && !server.IsRemote() {
			missing = append(missing, "a working directory (cwd)")
		}
		if oauth := server.OAuth; oauth != nil && server.IsRemote() {
			if oauth.ClientID != "" {
				value := map[string]any{"CLIENT_ID": oauth.ClientID}
				if oauth.ClientSecret != "" {
					value["CLIENT_SECRET"] = oauth.ClientSecret
				}
				if len(oauth.Scopes) != 0 {
					value["scopes"] = oauth.Scopes
				}
				object["auth"] = value
			} else if len(oauth.Scopes) != 0 {
				missing = append(missing, "OAuth scopes without a client id")
			}
			if oauth.CallbackPort != 0 {
				missing = append(missing, "an OAuth callback port")
			}
			if oauth.MetadataURL != "" {
				missing = append(missing, "an OAuth metadata URL")
			}
		}
		if len(missing) != 0 {
			warnings = append(warnings, mcp.NotCarried("Cursor", name, missing...))
		}
		servers[name] = object
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, warnings, fmt.Errorf("encode MCP servers: %w", err)
	}
	return data, warnings, nil
}
