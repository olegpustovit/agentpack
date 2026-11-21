package network

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

type agentSnapshot struct {
	Config       json.RawMessage    `json:"config"`
	Commands     json.RawMessage    `json:"commands,omitempty"`
	Capabilities *AgentCapabilities `json:"capabilities,omitempty"`
}

func (g *Gateway) serveAgentSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	owner := query.Get("owner")
	if owner == "" || owner == "local" {
		var err error
		owner, err = g.auth.store.NodeID(ctx)
		if err != nil {
			meshReadError(w, r, err)
			return
		}
	}
	var body string
	err := g.auth.store.Reader.QueryRowContext(ctx, `SELECT snapshot FROM agent_snapshots
		WHERE node_id=? AND harness=? AND definition_root=? AND mode=? ORDER BY updated_at DESC,agent_version DESC LIMIT 1`,
		owner, query.Get("harness"), query.Get("definition_root"), cmpOr(query.Get("mode"), "default")).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, nil)
		return
	}
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	writeJSON(w, json.RawMessage(body))
}

func persistAgentSnapshot(ctx context.Context, db *sql.DB, info SessionInfo, conv *ConversationSession) {
	conv.mu.Lock()
	snapshot := agentSnapshot{Config: conv.configJSONLocked(), Commands: conv.agentCommands, Capabilities: conv.agentCapabilities}
	version := conv.agentVersion
	conv.mu.Unlock()
	_, err := db.ExecContext(ctx, `INSERT INTO agent_snapshots(node_id,harness,definition_root,mode,agent_version,snapshot,updated_at)
		VALUES (`+state.LocalNodeSQL+`,?,?,?,?,?,?)
		ON CONFLICT DO UPDATE SET snapshot=excluded.snapshot,updated_at=excluded.updated_at WHERE snapshot<>excluded.snapshot`,
		info.Harness, cmpOr(info.DefinitionRoot, info.Directory), cmpOr(info.Mode, "default"), version, string(mustRawJSON(snapshot)), time.Now().UnixMilli())
	if err != nil {
		_, event := obs.Start(ctx, "worker", "save_agent_snapshot")
		event.Set("session_id", info.ID)
		event.Fail(err)
		event.End()
	}
}

type configSelection struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (selection configSelection) valid() bool {
	if len(selection.Key) == 0 || len(selection.Key) > 128 || len(selection.Value) > 4096 {
		return false
	}
	var value any
	if json.Unmarshal(selection.Value, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, bool:
		return true
	default:
		return false
	}
}

func (g *Gateway) serveWantedConfig(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if record, err := g.workerRecord(ctx, id); err == nil {
		if authorizeACPWrite(w, r, record.Session) {
			proxyWorkerConversation(w, r, record, "/config")
		}
		return
	}
	info, err := storedSession(ctx, g.auth.store.Reader, id)
	if err != nil || !validSessionID(id) || info.Transport != "acp" || info.ConversationState != "starting" || !info.EndedAt.IsZero() {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, info) {
		return
	}
	var input configSelection
	if !readJSON(w, r, &input) {
		return
	}
	if !input.valid() {
		http.Error(w, "valid key and value are required", http.StatusBadRequest)
		return
	}
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		configWriteError(w, r, err)
		return
	}
	lock, err := lockPromptHandoff(ctx, directory)
	if err != nil {
		configWriteError(w, r, err)
		return
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		_ = lock.Close()
		r.Body = io.NopCloser(bytes.NewReader(mustRawJSON(input)))
		if authorizeACPWrite(w, r, record.Session) {
			proxyWorkerConversation(w, r, record, "/config")
		}
		return
	}
	defer lock.Close()
	if err := saveWantedConfig(ctx, g.auth.store.DB, id, input, true); err != nil {
		configWriteError(w, r, err)
		return
	}
	writeJSON(w, map[string]bool{"accepted": true})
}

func configWriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errSessionUnavailable) {
		http.Error(w, "session unavailable", http.StatusConflict)
		return
	}
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "could not save config", http.StatusInternalServerError)
}

func saveWantedConfig(ctx context.Context, db *sql.DB, id string, choice configSelection, starting bool) error {
	patch := string(mustRawJSON(map[string]json.RawMessage{choice.Key: choice.Value}))
	query := `UPDATE sessions SET wanted_config=json_patch(wanted_config,?)
		WHERE id=? AND node_id=` + state.LocalNodeSQL + ` AND archived_at=0`
	if starting {
		query += " AND conversation_state='starting' AND ended_at=0"
	}
	result, err := db.ExecContext(ctx, query, patch, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return errSessionUnavailable
	}
	return err
}

func savedConfig(ctx context.Context, store *state.Store, info SessionInfo) (map[string]json.RawMessage, error) {
	var body string
	if err := store.Reader.QueryRowContext(ctx, "SELECT wanted_config FROM sessions WHERE id=?", info.ID).Scan(&body); err != nil {
		return nil, err
	}
	if body == "{}" {
		err := store.Reader.QueryRowContext(ctx, `SELECT wanted_config FROM sessions WHERE id<>? AND harness=?
			AND node_id=`+state.LocalNodeSQL+` AND owner_email=(SELECT owner_email FROM sessions WHERE id=?)
			AND coalesce(nullif(definition_root,''),directory)=? AND wanted_config<>'{}'
			ORDER BY last_used DESC LIMIT 1`, info.ID, info.Harness, info.ID, cmpOr(info.DefinitionRoot, info.Directory)).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		// Any choices made during startup win over the remembered defaults.
		if err := store.DB.QueryRowContext(ctx, "UPDATE sessions SET wanted_config=json_patch(?,wanted_config) WHERE id=? RETURNING wanted_config",
			body, info.ID).Scan(&body); err != nil {
			return nil, err
		}
	}
	var wanted map[string]json.RawMessage
	err := json.Unmarshal([]byte(body), &wanted)
	return wanted, err
}

type agentConfigChoice struct {
	ID      string              `json:"id"`
	Value   string              `json:"value"`
	Options []agentConfigChoice `json:"options"`
}

func hasConfigChoice(choices []agentConfigChoice, value string) bool {
	return slices.ContainsFunc(choices, func(choice agentConfigChoice) bool {
		return cmpOr(choice.Value, choice.ID) == value || hasConfigChoice(choice.Options, value)
	})
}

func (session *ConversationSession) wantedChoice(key string, value json.RawMessage) (allowed, unchanged bool) {
	var config struct {
		ConfigOptions []struct {
			ID      string              `json:"id"`
			Type    string              `json:"type"`
			Current json.RawMessage     `json:"currentValue"`
			Options []agentConfigChoice `json:"options"`
		} `json:"configOptions"`
		Modes struct {
			Current string              `json:"currentModeId"`
			Options []agentConfigChoice `json:"availableModes"`
		} `json:"modes"`
	}
	if json.Unmarshal(session.ConfigJSON(), &config) != nil {
		return false, false
	}
	var choice string
	for _, option := range config.ConfigOptions {
		if option.ID != key {
			continue
		}
		switch option.Type {
		case "select":
			allowed = json.Unmarshal(value, &choice) == nil && hasConfigChoice(option.Options, choice)
		case "boolean":
			var enabled bool
			allowed = json.Unmarshal(value, &enabled) == nil
		}
		return allowed, string(option.Current) == string(value)
	}
	if key == "mode" && json.Unmarshal(value, &choice) == nil {
		return hasConfigChoice(config.Modes.Options, choice), config.Modes.Current == choice
	}
	return false, false
}

func (session *ConversationSession) applyWantedConfig(ctx context.Context, wanted map[string]json.RawMessage) {
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// A model can change which effort levels the agent offers.
	if model := slices.Index(keys, "model"); model > 0 {
		keys[0], keys[model] = keys[model], keys[0]
	}
	for _, key := range keys {
		value := wanted[key]
		if allowed, unchanged := session.wantedChoice(key, value); allowed {
			if unchanged {
				continue
			}
			if err := session.SetConfigOption(ctx, key, value); err == nil {
				continue
			} else {
				_, event := obs.Start(ctx, "worker", "apply_saved_config")
				event.Set("session_id", session.sessionID)
				event.Fail(err)
				event.End()
			}
		}
		session.log.Append("notice", time.Now().UnixMilli(), mustRawJSON(map[string]string{
			"sessionUpdate": "notice", "severity": "info", "title": "A saved agent option could not be applied", "description": key,
		}))
	}
}

func (session *ConversationSession) logConfiguration() {
	session.log.Append("agent_configuration", time.Now().UnixMilli(), session.ConfigJSON())
}
