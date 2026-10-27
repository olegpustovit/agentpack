package network

import (
	"net/http"
	"runtime"
)

func (g *Gateway) registerSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		config, err := ReadSharingConfig(g.options.Home)
		if err != nil {
			respond(w, nil, err)
			return
		}
		writeJSON(w, map[string]any{"running": true, "platform": runtime.GOOS, "sharing_enabled": config.Enabled, "allowed_peers": config.Allow})
	})
	mux.HandleFunc("POST /api/settings/sharing", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		config, err := ReadSharingConfig(g.options.Home)
		if err == nil && input.Enabled {
			err = CheckTailnet(r.Context())
		}
		if err == nil {
			config.Enabled = input.Enabled
			err = WriteSharingConfig(g.options.Home, config)
		}
		respond(w, map[string]bool{"sharing_enabled": config.Enabled}, err)
	})
}
