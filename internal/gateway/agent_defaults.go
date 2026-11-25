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

type agentDefaults struct {
	AgentpackTools   bool `json:"agentpack_tools"`
	ReplaceSubagents bool `json:"replace_subagents"`
}

func readAgentDefaults(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (agentDefaults, error) {
	var result agentDefaults
	err := db.QueryRowContext(ctx, `SELECT
		coalesce(max(CASE WHEN key='agentpack_tools' THEN value END), 1),
		coalesce(max(CASE WHEN key='replace_subagents' THEN value END), 1)
		FROM agent_defaults WHERE key IN ('agentpack_tools','replace_subagents')`).
		Scan(&result.AgentpackTools, &result.ReplaceSubagents)
	return result, err
}

func (g *Gateway) registerAgentDefaults(mux routeRegistrar) {
	mux.HandleFunc("GET /api/agents/defaults", func(w http.ResponseWriter, r *http.Request) {
		result, err := readAgentDefaults(r.Context(), g.store.Reader)
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
		if key != "agentpack_tools" && key != "replace_subagents" || string(raw) != "true" && string(raw) != "false" {
			recordInvalidAgentDefaults(r)
			badRequest(w, "expected agentpack_tools or replace_subagents with boolean values")
			return
		}
	}

	ctx := r.Context()
	var result agentDefaults
	err := state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		for key, raw := range input {
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_defaults(key,value) VALUES (?,?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(raw) == "true"); err != nil {
				return err
			}
		}
		var err error
		result, err = readAgentDefaults(ctx, tx)
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
	defaults, err := readAgentDefaults(ctx, g.store.Reader)
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
