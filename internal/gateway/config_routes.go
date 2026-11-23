package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
)

func (g *Gateway) serveAgentSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	owner := query.Get("owner")
	if owner == "" || owner == "local" {
		var err error
		owner, err = g.store.NodeID(ctx)
		if err != nil {
			meshReadError(w, r, err)
			return
		}
	}
	var body string
	err := g.store.Reader.QueryRowContext(ctx, `SELECT snapshot FROM agent_snapshots
		WHERE node_id=? AND harness=? AND definition_root=? AND mode=? ORDER BY updated_at DESC,agent_version DESC LIMIT 1`,
		owner, query.Get("harness"), query.Get("definition_root"), network.CmpOr(query.Get("mode"), "default")).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		network.WriteJSON(w, nil)
		return
	}
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	network.WriteJSON(w, json.RawMessage(body))
}

func (g *Gateway) serveWantedConfig(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if record, err := g.workerRecord(ctx, id); err == nil {
		if authorizeACPWrite(w, r, record.Session) {
			network.ProxyWorkerConversation(w, r, record, "/config")
		}
		return
	}
	info, err := session.StoredSession(ctx, g.store.Reader, id)
	if err != nil || !network.ValidSessionID(id) || info.Transport != "acp" || info.ConversationState != "starting" || !info.EndedAt.IsZero() {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, info) {
		return
	}
	var input network.ConfigSelection
	if !network.ReadJSON(w, r, &input) {
		return
	}
	if !input.Valid() {
		http.Error(w, "valid key and value are required", http.StatusBadRequest)
		return
	}
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		network.ConfigWriteError(w, r, err)
		return
	}
	lock, err := network.LockPromptHandoff(ctx, directory)
	if err != nil {
		network.ConfigWriteError(w, r, err)
		return
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		_ = lock.Close()
		r.Body = io.NopCloser(bytes.NewReader(network.MustRawJSON(input)))
		if authorizeACPWrite(w, r, record.Session) {
			network.ProxyWorkerConversation(w, r, record, "/config")
		}
		return
	}
	defer lock.Close()
	if err := network.SaveWantedConfig(ctx, g.store.DB, id, input, true); err != nil {
		network.ConfigWriteError(w, r, err)
		return
	}
	network.WriteJSON(w, map[string]bool{"accepted": true})
}
