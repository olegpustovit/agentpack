package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (g *Gateway) registerAgentDefaults(mux routeRegistrar) {
	mux.HandleFunc("GET /api/agents/defaults", func(w http.ResponseWriter, r *http.Request) {
		result, err := state.ReadAgentDefaults(r.Context(), g.store.Reader)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.WriteJSON(w, result)
	})
	mux.HandleFunc("PUT /api/agents/defaults", g.updateAgentDefaults)
}

func (g *Gateway) updateAgentDefaults(w http.ResponseWriter, r *http.Request) {
	var input map[string]json.RawMessage
	valid := network.ReadJSON(w, r, &input)
	if !valid {
		recordInvalidAgentDefaults(r)
		return
	}
	if input == nil {
		recordInvalidAgentDefaults(r)
		badRequest(w, "expected a JSON object")
		return
	}
	for key, raw := range input {
		switch key {
		case "agentpack_tools", "replace_subagents", "bypass_permissions", "auto_approve_helpers":
			if string(raw) == "true" || string(raw) == "false" {
				continue
			}
		}
		recordInvalidAgentDefaults(r)
		badRequest(w, "expected agent defaults with boolean values")
		return
	}

	ctx := r.Context()
	var result state.AgentDefaults
	err := state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		for key, raw := range input {
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_defaults(key,value) VALUES (?,?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(raw) == "true"); err != nil {
				return err
			}
		}
		var err error
		result, err = state.ReadAgentDefaults(ctx, tx)
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	network.WriteJSON(w, result)
}

func recordInvalidAgentDefaults(r *http.Request) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(errors.New("invalid agent defaults request"))
	}
}

func (g *Gateway) applyAgentDefaults(ctx context.Context, info *session.SessionInfo, tools *bool) error {
	if info.Transport != "acp" {
		return nil
	}
	defaults, err := state.ReadAgentDefaults(ctx, g.store.Reader)
	if err != nil {
		return err
	}
	info.Tools = defaults.AgentpackTools
	if tools != nil {
		info.Tools = *tools
	}
	info.KeepNativeSubagents = !defaults.ReplaceSubagents
	return nil
}
