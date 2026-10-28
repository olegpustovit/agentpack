package claude

import (
	"encoding/json"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// writeLSP puts the language servers in .lsp.json at the bundle root, where
// Claude Code reads a plugin's language servers.
func writeLSP(servers map[string]json.RawMessage, ctx base.StageContext) error {
	projection, err := planLSP(servers, ctx)
	if err != nil {
		return err
	}
	return base.WriteLSPProjection(projection)
}

func planLSP(servers map[string]json.RawMessage, ctx base.StageContext) (base.LSPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.LSPProjection{}, err
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return base.LSPProjection{}, err
	}
	data = append(data, '\n')
	return base.LSPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, ".lsp.json"), Contents: data, Scope: "lsp"}}}, nil
}
