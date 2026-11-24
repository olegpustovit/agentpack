package cursor

import "github.com/olegpustovit/agentpack/internal/mcp"

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	return mcp.RenderJSONServers(entries, "Cursor", func(server mcp.Server) (map[string]any, []string) {
		object := server.JSONObject("")
		var missing []string
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
		return object, missing
	})
}
