package network

import (
	"net/http"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/agy"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/opencode"
)

func (g *Gateway) registerLimits(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		snapshots := []harness.LimitSnapshot{
			codex.ReadLimits(r.Context(), ""),
			claude.ReadLimits(r.Context()),
			agy.ReadLimits(r.Context()),
			grok.ReadLimits(r.Context()),
			opencode.ReadLimits(r.Context()),
		}
		writeJSON(w, map[string]any{"limits": snapshots})
	})
}
