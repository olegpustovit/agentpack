package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

type agentSnapshot struct {
	Config       json.RawMessage            `json:"config"`
	Commands     json.RawMessage            `json:"commands,omitempty"`
	Capabilities *session.AgentCapabilities `json:"capabilities,omitempty"`
}

func persistAgentSnapshot(ctx context.Context, db *sql.DB, info session.SessionInfo, conv *ConversationSession) {
	conv.mu.Lock()
	snapshot := agentSnapshot{Config: conv.configJSONLocked(), Commands: conv.agentCommands, Capabilities: conv.agentCapabilities}
	version := conv.agentVersion
	conv.mu.Unlock()
	_, err := db.ExecContext(ctx, `INSERT INTO agent_snapshots(node_id,harness,definition_root,mode,agent_version,snapshot,updated_at)
		VALUES (`+state.LocalNodeSQL+`,?,?,?,?,?,?)
		ON CONFLICT DO UPDATE SET snapshot=excluded.snapshot,updated_at=excluded.updated_at WHERE snapshot<>excluded.snapshot`,
		info.Harness, CmpOr(info.DefinitionRoot, info.Directory), CmpOr(info.Mode, "default"), version, string(MustRawJSON(snapshot)), time.Now().UnixMilli())
	if err != nil {
		_, event := obs.Start(ctx, "worker", "save_agent_snapshot")
		event.Set("session_id", info.ID)
		event.Fail(err)
		event.End()
	}
}

type ConfigSelection struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// ChatStartOptions are ACP choice IDs to apply before sending the first message.
type ChatStartOptions struct {
	Model  string `json:"model,omitempty" jsonschema:"Model choice ID from list_chat_options, for a chat."`
	Effort string `json:"effort,omitempty" jsonschema:"Reasoning effort choice ID from list_chat_options, for a chat."`
}

func (options ChatStartOptions) Valid() bool {
	for _, value := range []string{options.Model, options.Effort} {
		if len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
			return false
		}
	}
	return true
}

// Kept with the other saved choices until the first agent accepts them. The reserved key is
// replaced with the agent's real option IDs once startup succeeds.
const startOptionsKey = "$start"

func SaveChatStartOptions(ctx context.Context, tx *sql.Tx, id string, options ChatStartOptions) error {
	if options == (ChatStartOptions{}) {
		return nil
	}
	_, err := tx.ExecContext(ctx, "UPDATE sessions SET wanted_config=json_patch(wanted_config,?) WHERE id=?",
		string(MustRawJSON(map[string]ChatStartOptions{startOptionsKey: options})), id)
	return err
}

func (selection ConfigSelection) Valid() bool {
	if len(selection.Key) == 0 || len(selection.Key) > 128 || selection.Key == startOptionsKey || len(selection.Value) > 4096 {
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

func ConfigWriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, session.ErrSessionUnavailable) {
		http.Error(w, "session unavailable", http.StatusConflict)
		return
	}
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "could not save config", http.StatusInternalServerError)
}

func SaveWantedConfig(ctx context.Context, db *sql.DB, id string, choice ConfigSelection, starting bool) error {
	patch := string(MustRawJSON(map[string]json.RawMessage{choice.Key: choice.Value}))
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
		return session.ErrSessionUnavailable
	}
	return err
}

func savedConfig(ctx context.Context, store *state.Store, info session.SessionInfo, excludeInheritedKey string) (map[string]json.RawMessage, error) {
	var body string
	if err := store.Reader.QueryRowContext(ctx, "SELECT wanted_config FROM sessions WHERE id=?", info.ID).Scan(&body); err != nil {
		return nil, err
	}
	if body == "{}" {
		err := store.Reader.QueryRowContext(ctx, `SELECT wanted_config FROM sessions WHERE id<>? AND harness=?
			AND node_id=`+state.LocalNodeSQL+` AND owner_email=(SELECT owner_email FROM sessions WHERE id=?)
			AND coalesce(nullif(definition_root,''),directory)=? AND wanted_config<>'{}'
			AND json_type(wanted_config,'$."$start"') IS NULL
			ORDER BY last_used DESC LIMIT 1`, info.ID, info.Harness, info.ID, CmpOr(info.DefinitionRoot, info.Directory)).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		// Keep this chat's own choices, including choices made while it starts.
		exclude := "{}"
		if excludeInheritedKey != "" {
			exclude = string(MustRawJSON(map[string]any{excludeInheritedKey: nil}))
		}
		if err := store.DB.QueryRowContext(ctx, "UPDATE sessions SET wanted_config=json_patch(json_patch(?,?),wanted_config) WHERE id=? RETURNING wanted_config",
			body, exclude, info.ID).Scan(&body); err != nil {
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

type agentConfigOption struct {
	ID       string              `json:"id"`
	Category string              `json:"category"`
	Type     string              `json:"type"`
	Current  json.RawMessage     `json:"currentValue"`
	Options  []agentConfigChoice `json:"options"`
}

var configCategory = strings.NewReplacer("_", "", "-", "", " ", "")

func (option agentConfigOption) role() string {
	switch configCategory.Replace(strings.ToLower(CmpOr(option.Category, option.ID))) {
	case "model":
		return "model"
	case "thoughtlevel", "reasoningeffort", "effort":
		return "effort"
	}
	return ""
}

func hasConfigChoice(choices []agentConfigChoice, value string) bool {
	return slices.ContainsFunc(choices, func(choice agentConfigChoice) bool {
		return CmpOr(choice.Value, choice.ID) == value || hasConfigChoice(choice.Options, value)
	})
}

func (cs *ConversationSession) wantedChoice(key string, value json.RawMessage) (allowed, unchanged bool) {
	var config struct {
		ConfigOptions []agentConfigOption `json:"configOptions"`
		Modes         struct {
			Current string              `json:"currentModeId"`
			Options []agentConfigChoice `json:"availableModes"`
		} `json:"modes"`
	}
	if json.Unmarshal(cs.ConfigJSON(), &config) != nil {
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

func (cs *ConversationSession) startOptionKey(role string) string {
	var config struct {
		Options []agentConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(cs.ConfigJSON(), &config) == nil {
		for _, option := range config.Options {
			if option.Type == "select" && option.role() == role {
				return option.ID
			}
		}
	}
	return ""
}

func (cs *ConversationSession) applyStartOptions(ctx context.Context, db *sql.DB, id string, wanted map[string]json.RawMessage) error {
	raw, pending := wanted[startOptionsKey]
	if !pending {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var choices ChatStartOptions
	if err := json.Unmarshal(raw, &choices); err != nil {
		return errors.New("could not read the selected chat options")
	}
	patch := map[string]json.RawMessage{startOptionsKey: json.RawMessage("null")}
	for _, choice := range []struct{ role, value string }{{"model", choices.Model}, {"effort", choices.Effort}} {
		if choice.value == "" {
			continue
		}
		key := cs.startOptionKey(choice.role)
		value := wanted[key]
		if len(value) == 0 {
			value = MustRawJSON(choice.value)
		}
		allowed, unchanged := cs.wantedChoice(key, value)
		if key == "" || !allowed || !unchanged && cs.SetConfigOption(ctx, key, value) != nil {
			return fmt.Errorf("could not apply the selected %s. Start a new chat with an available choice", choice.role)
		}
		patch[key] = value
		delete(wanted, key)
	}
	delete(wanted, startOptionsKey)
	_, err := db.ExecContext(ctx, "UPDATE sessions SET wanted_config=json_patch(wanted_config,?) WHERE id=?",
		string(MustRawJSON(patch)), id)
	return err
}

func (cs *ConversationSession) applyWantedConfig(ctx context.Context, wanted map[string]json.RawMessage) {
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// A model can change which effort levels the agent offers.
	if len(keys) > 1 {
		if model := slices.Index(keys, cs.startOptionKey("model")); model > 0 {
			keys[0], keys[model] = keys[model], keys[0]
		}
	}
	for _, key := range keys {
		value := wanted[key]
		if allowed, unchanged := cs.wantedChoice(key, value); allowed {
			if unchanged {
				continue
			}
			if err := cs.SetConfigOption(ctx, key, value); err == nil {
				continue
			} else {
				_, event := obs.Start(ctx, "worker", "apply_saved_config")
				event.Set("session_id", cs.sessionID)
				event.Fail(err)
				event.End()
			}
		}
		cs.Log.Append("notice", time.Now().UnixMilli(), MustRawJSON(map[string]string{
			"sessionUpdate": "notice", "severity": "info", "title": "A saved agent option could not be applied", "description": key,
		}))
	}
}

func (cs *ConversationSession) logConfiguration() {
	cs.Log.Append("agent_configuration", time.Now().UnixMilli(), cs.ConfigJSON())
}
