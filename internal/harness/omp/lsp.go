package omp

import (
	"encoding/json"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func planLSP(servers map[string]json.RawMessage, ctx base.StageContext) (base.LSPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.LSPProjection{}, err
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return base.LSPProjection{}, err
	}
	return base.LSPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, ".lsp.json"), Contents: append(data, '\n'), Scope: "lsp"}}}, nil
}

func writeLSP(servers map[string]json.RawMessage, ctx base.StageContext) error {
	projection, err := planLSP(servers, ctx)
	if err != nil {
		return err
	}
	return base.WriteLSPProjection(projection)
}
