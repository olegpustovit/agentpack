package network

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// Liveness follows successful sync contacts, never replicated wall-clock heartbeat rows.
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
	if lastSync.IsZero() && contact.Error == "" {
		lastSync = contact.At
	}
	return contact.Error == "" && time.Since(contact.At) < 90*time.Second, lastSync
}

func (g *Gateway) meshSessions(ctx context.Context, node string) ([]SessionInfo, error) {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + sessionColumns + " FROM sessions WHERE hidden=0 AND (ended_at=0 OR transport='acp')"
	var args []any
	if node != "" {
		query += " AND node_id=?"
		args = append(args, node)
	}
	entries, err := querySessions(ctx, g.auth.store.Reader, query+" ORDER BY last_used DESC,id DESC", args...)
	if err != nil {
		return nil, err
	}
	localEntries := slices.DeleteFunc(slices.Clone(entries), func(info SessionInfo) bool { return info.NodeID != local })
	states := workerStates(ctx, g.options.Home, localEntries)
	for i := range entries {
		info := &entries[i]
		info.Reachable, info.LastSync = g.nodeContact(info.NodeID, local)
		if info.NodeID == local {
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

func (g *Gateway) meshRecent(ctx context.Context, node string) ([]RecentFolder, error) {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	filter := ""
	var args []any
	if node != "" {
		filter = " AND node_id=?"
		args = append(args, node)
	}
	entries, err := querySessions(ctx, g.auth.store.Reader, `SELECT `+sessionColumns+` FROM (
 SELECT *,row_number() OVER (PARTITION BY node_id,directory,harness,mode ORDER BY last_used DESC,id DESC) AS rank
 FROM sessions WHERE hidden=0 AND directory<>''`+filter+`) WHERE rank=1 ORDER BY last_used DESC,id DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	folders := make([]RecentFolder, 0, len(entries))
	for _, info := range entries {
		info.ID = ""
		info.State = TerminalState{}
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
			case info.FolderID == "" || identity != info.FolderID:
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

// Route mutations and live streams to the owning node; keep replicated reads on this gateway.
func (g *Gateway) routeMeshRequest(w http.ResponseWriter, r *http.Request) (bool, error) {
	node := r.URL.Query().Get("node")
	if r.Method == http.MethodGet && meshListRoute(r.URL.Path) {
		return false, nil // List handlers select replicated rows; legacy peers retain live proxying.
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "sessions" {
		return false, nil
	}
	info, err := storedSession(r.Context(), g.auth.store.Reader, parts[2])
	if errors.Is(err, errSessionNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.ArchivedAt.IsZero() && r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "archived", http.StatusConflict)
		return true, nil
	}
	if !info.ArchivedAt.IsZero() && r.Method == http.MethodGet && len(parts) == 4 && parts[3] == "conversation" {
		r.SetPathValue("id", parts[2])
		g.serveConversation(w, r)
		return true, nil
	}
	if r.Method == http.MethodGet && len(parts) == 5 && parts[3] == "blobs" {
		r.SetPathValue("id", parts[2])
		r.SetPathValue("sha", parts[4])
		g.serveBlob(w, r)
		return true, nil
	}
	local, err := g.auth.store.NodeID(r.Context())
	if err != nil {
		return false, err
	}
	if info.NodeID == local {
		return false, nil
	}
	if node != "" && node != info.NodeID {
		http.Error(w, "session belongs to another node", http.StatusBadRequest)
		return true, nil
	}
	if r.Method == http.MethodPatch && len(parts) == 3 {
		query := r.URL.Query()
		query.Del("node")
		r.URL.RawQuery = query.Encode()
		return false, nil
	}
	r.SetPathValue("id", parts[2])
	online, _ := g.nodeContact(info.NodeID, local)
	history := r.Method == http.MethodGet && len(parts) == 4 && parts[3] == "conversation"
	if history && !online {
		r.SetPathValue("id", parts[2])
		g.serveConversation(w, r)
		return true, nil
	}
	if !online {
		http.Error(w, "owning machine is offline", http.StatusServiceUnavailable)
		return true, nil
	}
	g.proxyTailnet(w, r, info.NodeID)
	return true, nil
}

func (g *Gateway) meshAgentSessions(ctx context.Context, node, directory, harness string) ([]AgentSession, error) {
	entries, err := g.meshSessions(ctx, node)
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
