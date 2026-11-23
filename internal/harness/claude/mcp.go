package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	servers := make(map[string]map[string]any)
	var warnings []string
	for _, name := range entries.Names() {
		server := entries[name].Server
		if server.Disabled != nil && *server.Disabled {
			continue
		}
		server.Disabled = nil
		if server.IsRemote() && server.Type == nil {
			kind := "http"
			server.Type = &kind
		}
		object := server.JSONObject()
		var missing []string
		if server.Cwd != nil && !server.IsRemote() {
			missing = append(missing, "a working directory (cwd)")
		}
		if oauth := server.OAuth; oauth != nil && server.IsRemote() {
			value := make(map[string]any)
			if oauth.ClientID != "" {
				value["clientId"] = oauth.ClientID
			}
			if oauth.CallbackPort != 0 {
				value["callbackPort"] = oauth.CallbackPort
			}
			if oauth.MetadataURL != "" {
				value["authServerMetadataUrl"] = oauth.MetadataURL
			}
			if len(oauth.Scopes) != 0 {
				value["scopes"] = strings.Join(oauth.Scopes, " ")
			}
			if len(value) != 0 {
				object["oauth"] = value
			}
			if oauth.ClientSecret != "" {
				missing = append(missing, "an OAuth client secret in the file (set MCP_CLIENT_SECRET or use claude mcp add --client-secret)")
			}
		}
		if len(missing) != 0 {
			warnings = append(warnings, mcp.NotCarried("Claude Code", name, missing...))
		}
		servers[name] = object
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, warnings, fmt.Errorf("encode MCP servers: %w", err)
	}
	return data, warnings, nil
}
