package omp

import (
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

func RenderMCP(entries mcp.Entries) ([]byte, []string, error) {
	return mcp.RenderJSONServers(entries, "OMP", func(server mcp.Server) (map[string]any, []string) {
		object := server.JSONObject("http")
		delete(object, "disabled")
		if server.Disabled != nil {
			object["enabled"] = !*server.Disabled
		}
		if server.Cwd != nil && !server.IsRemote() {
			object["cwd"] = *server.Cwd
		}
		var missing []string
		if oauth := server.OAuth; oauth != nil && server.IsRemote() {
			value := oauth.JSONClient()
			if oauth.CallbackPort != 0 {
				value["callbackPort"] = oauth.CallbackPort
			}
			if len(value) != 0 {
				object["oauth"] = value
			}
			if oauth.MetadataURL != "" {
				missing = append(missing, "an OAuth metadata URL")
			}
		}
		return object, missing
	})
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
	return base.NewMCPProjection(entries, base.MCPProjectedFile{
		Path: filepath.Join(root, ".mcp.json"), Contents: data, Scope: "mcpServers",
	}, warnings, nil, nil)
}
