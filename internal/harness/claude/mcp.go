package claude

import (
	"strings"

	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	return mcp.RenderJSONServers(entries, "Claude Code", func(server mcp.Server) (map[string]any, []string) {
		if server.Disabled != nil && *server.Disabled {
			return nil, nil
		}
		server.Disabled = nil
		object := server.JSONObject("http")
		var missing []string
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
		return object, missing
	})
}
