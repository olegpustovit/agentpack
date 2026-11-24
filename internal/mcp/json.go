package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderJSONServers writes a standalone mcpServers config.
func RenderJSONServers(entries Entries, agent string, project func(Server) (map[string]any, []string)) ([]byte, []string, error) {
	servers := make(map[string]any, len(entries))
	warnings, _ := MergeJSONServers(servers, entries, agent, project)
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, warnings, fmt.Errorf("encode MCP servers: %w", err)
	}
	return data, warnings, nil
}

// MergeJSONServers adds missing names in sorted order. A nil projection omits
// a server, and unsupported fields are reported in the agent's staging warnings.
func MergeJSONServers(servers map[string]any, entries Entries, agent string, project func(Server) (map[string]any, []string)) (warnings, shadowed []string) {
	for _, name := range entries.Names() {
		if _, exists := servers[name]; exists {
			shadowed = append(shadowed, name)
			continue
		}
		server := entries[name].Server
		object, missing := project(server)
		if object == nil {
			continue
		}
		if server.Cwd != nil && !server.IsRemote() && object["cwd"] == nil {
			missing = append(missing, "a working directory (cwd)")
		}
		if len(missing) != 0 {
			warnings = append(warnings, NotCarried(agent, name, missing...))
		}
		servers[name] = object
	}
	return warnings, shadowed
}

// JSONClient renders the OAuth fields shared by OMP and OpenCode.
func (oauth OAuth) JSONClient() map[string]any {
	client := make(map[string]any)
	if oauth.ClientID != "" {
		client["clientId"] = oauth.ClientID
	}
	if oauth.ClientSecret != "" {
		client["clientSecret"] = oauth.ClientSecret
	}
	if len(oauth.Scopes) != 0 {
		client["scope"] = strings.Join(oauth.Scopes, " ")
	}
	return client
}
