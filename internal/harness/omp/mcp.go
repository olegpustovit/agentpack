package omp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	servers := make(map[string]any)
	var warnings []string
	for _, name := range entries.Names() {
		server := entries[name].Server
		object := server.JSONObject()
		delete(object, "disabled")
		if server.Disabled != nil {
			object["enabled"] = !*server.Disabled
		}
		if server.Cwd != nil && !server.IsRemote() {
			object["cwd"] = *server.Cwd
		}
		if server.IsRemote() && server.Type == nil {
			object["type"] = "http"
		}
		if oauth := server.OAuth; oauth != nil && server.IsRemote() {
			value := make(map[string]any)
			if oauth.ClientID != "" {
				value["clientId"] = oauth.ClientID
			}
			if oauth.ClientSecret != "" {
				value["clientSecret"] = oauth.ClientSecret
			}
			if oauth.CallbackPort != 0 {
				value["callbackPort"] = oauth.CallbackPort
			}
			if len(oauth.Scopes) != 0 {
				value["scope"] = strings.Join(oauth.Scopes, " ")
			}
			if len(value) != 0 {
				object["oauth"] = value
			}
			if oauth.MetadataURL != "" {
				warnings = append(warnings, mcp.NotCarried("OMP", name, "an OAuth metadata URL"))
			}
		}
		servers[name] = object
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	return data, warnings, err
}

func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	data, warnings, err := RenderMCP(entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	digest, err := base.MCPJSONScopeDigest(data, "mcpServers")
	if err != nil {
		return base.MCPProjection{}, err
	}
	servers := base.ProjectedMCPServers(entries)
	base.MarkMCPWarnings(servers, warnings)
	return base.MCPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, ".mcp.json"), Contents: data, Scope: "mcpServers", ScopeDigest: digest, MergeOrder: []string{"plugin definitions", "agentpack.toml", ".agents/mcp.json"}}}, Servers: servers, Warnings: warnings}, nil
}
