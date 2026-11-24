package agy

import "github.com/olegpustovit/agentpack/internal/mcp"

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	return mcp.RenderJSONServers(entries, "Antigravity", func(server mcp.Server) (map[string]any, []string) {
		value := make(map[string]any)
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
		return value, missing
	})
}
