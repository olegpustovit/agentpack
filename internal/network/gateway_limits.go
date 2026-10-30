package network

import (
	"net/http"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
)

func (g *Gateway) registerLimits(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		snapshots := []harness.LimitSnapshot{
			codex.ReadLimits(r.Context(), ""),
			opencode.ReadLimits(r.Context()),
		}
		writeJSON(w, map[string]any{"limits": snapshots})
	})
}
