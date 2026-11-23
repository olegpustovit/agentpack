package network

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

// Liveness follows recent HTTP responses from the peer.
func (g *Gateway) nodeContact(node, local string) (bool, time.Time) {
	if node == local {
		return true, time.Time{}
	}
	value, ok := g.replicaStatus.Load(node)
	if !ok {
		return false, time.Time{}
	}
	contact := value.(replicaContact)
	lastSync := contact.LastSuccess
	if lastSync.IsZero() && contact.Reachable && contact.Error == "" {
		lastSync = contact.At
	}
	return contact.Reachable && time.Since(contact.At) < 90*time.Second, lastSync
}

func (g *Gateway) meshSessions(ctx context.Context, node string, recentOnly bool) ([]session.SessionInfo, error) {
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	where := "hidden=0 AND (transport<>'shell' OR parent_id='')"
	var args []any
	if node != "" {
		where += " AND node_id=?"
		args = append(args, node)
	}
	query := "SELECT " + session.SessionColumns + " FROM sessions WHERE " + where + " AND (ended_at=0 OR transport='acp')"
	if recentOnly {
		query += " AND (ended_at=0 OR id IN (SELECT id FROM sessions WHERE " + where +
			" AND ended_at>0 AND transport='acp' ORDER BY last_used DESC,id DESC LIMIT 100))"
		if node != "" {
			args = append(args, node)
		}
	}
	entries, err := session.QuerySessions(ctx, g.store.Reader, query+" ORDER BY last_used DESC,id DESC", args...)
	if err != nil {
		return nil, err
	}
	localEntries := slices.DeleteFunc(slices.Clone(entries), func(info session.SessionInfo) bool {
		return info.NodeID != local || info.Transport == "acp" || !info.EndedAt.IsZero()
	})
	states := workerStates(ctx, g.options.Home, localEntries)
	for i := range entries {
		info := &entries[i]
		info.Reachable, info.LastSync = g.nodeContact(info.NodeID, local)
		if info.NodeID == local && info.Transport != "acp" {
			if terminal, ok := states[info.ID]; ok {
				info.State = terminal
			} else {
				info.State.Running = false
			}
		}
		if !info.EndedAt.IsZero() {
			info.State.Running = false
			info.State.Restarting = false
		}
		*info = publicSession(*info)
	}
	return entries, nil
}

// An older chat can fall out of the recent list while its bookmark still works.
func (g *Gateway) serveSessionRow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info, err := session.ScanSession(g.store.Reader.QueryRowContext(ctx, "SELECT "+session.SessionColumns+" FROM sessions WHERE id=? AND hidden=0", r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	if node := r.URL.Query().Get("node"); node != "" && node != info.NodeID {
		http.Error(w, "session belongs to another node", http.StatusBadRequest)
		return
	}
	local, err := g.store.NodeID(ctx)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	info.Reachable, info.LastSync = g.nodeContact(info.NodeID, local)
	if !info.EndedAt.IsZero() {
		info.State.Running, info.State.Restarting = false, false
	}
	writeJSON(w, publicSession(info))
}

func (g *Gateway) meshRecent(ctx context.Context, node string) ([]RecentFolder, error) {
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	filter := ""
	var args []any
	if node != "" {
		filter = " AND node_id=?"
		args = append(args, node)
	}
	entries, err := session.QuerySessions(ctx, g.store.Reader, `SELECT `+session.SessionColumns+` FROM (
 SELECT *,row_number() OVER (PARTITION BY node_id,directory,harness,mode ORDER BY last_used DESC,id DESC) AS rank
 FROM sessions WHERE hidden=0 AND transport<>'shell' AND directory<>''`+filter+`) WHERE rank=1 ORDER BY last_used DESC,id DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	folders := make([]RecentFolder, 0, len(entries))
	for _, info := range entries {
		info.ID = ""
		info.State = session.TerminalState{}
		info.Reachable, info.LastSync = g.nodeContact(info.NodeID, local)
		folder := RecentFolder{SessionInfo: publicSession(info)}
		if info.NodeID != local {
			folder.Available = info.Reachable
			if !info.Reachable {
				folder.UnavailableReason = "Machine is offline"
			}
		} else {
			identity, err := FolderIdentity(info.Directory)
			switch {
			case err != nil:
				folder.UnavailableReason = "Folder is missing or inaccessible"
			case !SameFolder(info.FolderID, identity):
				folder.UnavailableReason = "Folder changed since its last local launch"
			default:
				folder.Available = true
			}
		}
		folders = append(folders, folder)
	}
	return folders, nil
}

func meshListRoute(path string) bool {
	return path == "/api/sessions" || path == "/api/recent" || path == "/api/limits" || path == "/api/agent-sessions"
}

// routeMeshRequest resolves a session route's row once and keeps it in the request context, then
// routes mutations and live streams to the owning node; replicated reads stay on this gateway.
func (g *Gateway) routeMeshRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool, error) {
	node := r.URL.Query().Get("node")
	info, err := session.StoredSession(r.Context(), g.store.Reader, r.PathValue("id"))
	if errors.Is(err, session.ErrSessionNotFound) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r = r.WithContext(context.WithValue(r.Context(), sessionRowKey{}, info))
	if !info.ArchivedAt.IsZero() && r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "archived", http.StatusConflict)
		return r, true, nil
	}
	history := r.Pattern == "GET /api/sessions/{id}/conversation"
	if history && (!info.ArchivedAt.IsZero() || !isConversationStream(r)) {
		if node != "" && node != info.NodeID {
			http.Error(w, "session belongs to another node", http.StatusBadRequest)
			return r, true, nil
		}
		g.serveStoredSessionConversation(w, r, info, false)
		return r, true, nil
	}
	if r.Pattern == "GET /api/sessions/{id}/blobs/{sha}" {
		g.serveBlob(w, r)
		return r, true, nil
	}
	local, err := g.nodeID(r.Context())
	if err != nil {
		return r, false, err
	}
	if info.NodeID == local {
		if history {
			g.serveStoredSessionConversation(w, r, info, true)
			return r, true, nil
		}
		return r, false, nil
	}
	if node != "" && node != info.NodeID {
		http.Error(w, "session belongs to another node", http.StatusBadRequest)
		return r, true, nil
	}
	if r.Pattern == "PATCH /api/sessions/{id}" {
		query := r.URL.Query()
		query.Del("node")
		r.URL.RawQuery = query.Encode()
		return r, false, nil
	}
	online, _ := g.nodeContact(info.NodeID, local)
	if history && !online {
		g.serveStoredSessionConversation(w, r, info, false)
		return r, true, nil
	}
	if !online {
		http.Error(w, "owning machine is offline", http.StatusServiceUnavailable)
		return r, true, nil
	}
	g.proxyTailnet(w, r, info.NodeID)
	return r, true, nil
}

func (g *Gateway) meshAgentSessions(ctx context.Context, node, directory, harness string) ([]AgentSession, error) {
	entries, err := g.meshSessions(ctx, node, false)
	if err != nil {
		return nil, err
	}
	result := []AgentSession{}
	seen := map[string]bool{}
	for _, info := range entries {
		key := info.NodeID + "\x00" + info.AgentSessionID
		if !info.ArchivedAt.IsZero() || info.AgentSessionID == "" || seen[key] || directory != "" && filepath.Clean(info.Directory) != filepath.Clean(directory) || harness != "" && info.Harness != harness {
			continue
		}
		seen[key] = true
		result = append(result, AgentSession{ID: info.AgentSessionID, Title: info.Name, Cwd: info.Directory, NodeID: info.NodeID, Reachable: info.Reachable})
	}
	return result, nil
}

func meshReadError(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "replicated state unavailable", http.StatusInternalServerError)
}
