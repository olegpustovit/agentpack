package gateway

import (
	"github.com/olegpustovit/agentpack/internal/network"
	"net/http"
	"runtime"
)

func (g *Gateway) registerSettings(mux routeRegistrar) {
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := ReadSharingConfig(g.store)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]any{"running": true, "platform": runtime.GOOS, "sharing_enabled": config.Enabled, "allowed_peers": config.Allow, "public": g.publicStatus(r.Context())})
	})
	mux.HandleFunc("POST /api/settings/public", g.setPublic)
	mux.HandleFunc("POST /api/settings/sharing", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		config, err := ReadSharingConfig(g.store)
		if err == nil && input.Enabled {
			err = CheckTailnet(r.Context())
		}
		if err == nil {
			config.Enabled = input.Enabled
			err = WriteSharingConfig(g.store, config)
		}
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, map[string]bool{"sharing_enabled": config.Enabled})
	})
}
