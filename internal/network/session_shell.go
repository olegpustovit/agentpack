package network

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (g *Gateway) startShell(w http.ResponseWriter, r *http.Request, actor Actor, input chatStartRequest) {
	ctx := r.Context()
	node, err := g.auth.store.NodeID(ctx)
	if err != nil {
		writeSessionStart(w, SessionInfo{}, err)
		return
	}
	info := SessionInfo{NodeID: node, Reachable: true, Transport: "shell", Harness: "shell", ParentID: input.ParentID, StartedBy: &actor}
	if input.ParentID != "" {
		parent, err := storedSession(ctx, g.auth.store.Reader, input.ParentID)
		if errors.Is(err, errSessionNotFound) {
			http.Error(w, "chat not found", http.StatusNotFound)
			return
		}
		if err != nil {
			meshReadError(w, r, err)
			return
		}
		if parent.Transport != "acp" || parent.NodeID != node {
			http.Error(w, "choose a chat on this machine", http.StatusBadRequest)
			return
		}
		if !authorizeACPWrite(w, r, parent) {
			return
		}
		switch input.Place {
		case "", "chat":
			info.Directory, info.WorktreeOf = parent.Directory, parent.WorktreeOf
			if identity, err := FolderIdentity(info.Directory); err != nil || !sameFolder(parent.FolderID, identity) {
				writeSessionStart(w, info, errors.New("the chat folder has changed"))
				return
			}
		case "main":
			info.Directory = cmpOr(parent.WorktreeOf, parent.Directory)
		case "home":
			info.Directory, err = os.UserHomeDir()
			info.Title = "Home"
		default:
			err = errors.New("choose the chat, main or home folder")
		}
	} else {
		if !g.ownerOnly(w, r) {
			return
		}
		info.Directory = input.Directory
		if input.Place == "home" {
			info.Directory, err = os.UserHomeDir()
			info.Title = "Home"
		}
	}
	if err == nil && !filepath.IsAbs(info.Directory) {
		err = errors.New("folder must be absolute")
	}
	if err == nil {
		info.Directory = filepath.Clean(info.Directory)
		info, err = g.launchSession(ctx, info, "")
		if err != nil {
			obs.From(ctx).Fail(err)
		}
	}
	writeSessionStart(w, info, err)
}

func (g *Gateway) serveChatShells(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parent, err := storedSession(ctx, g.auth.store.Reader, r.PathValue("id"))
	if errors.Is(err, errSessionNotFound) {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	if parent.Transport != "acp" {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, parent) {
		return
	}
	rows, err := querySessions(ctx, g.auth.store.Reader, "SELECT "+sessionColumns+
		" FROM sessions WHERE transport='shell' AND parent_id=? AND node_id=? AND hidden=0 AND ended_at=0 ORDER BY started_at,id", parent.ID, parent.NodeID)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	for i := range rows {
		rows[i] = publicSession(rows[i])
	}
	writeJSON(w, rows)
}

// Detaching moves the existing PTY into the session list without changing the worker or its owner.
func (g *Gateway) detachShell(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !g.authorizeACPWriteByID(w, r, id) {
		return
	}
	result, err := g.auth.store.DB.ExecContext(ctx, "UPDATE sessions SET parent_id='' WHERE id=? AND transport='shell' AND ended_at=0 AND node_id="+state.LocalNodeSQL, id)
	if err == nil {
		var changed int64
		changed, err = result.RowsAffected()
		if err == nil && changed == 0 {
			http.Error(w, "terminal has ended", http.StatusConflict)
			return
		}
	}
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	writeJSON(w, map[string]bool{"detached": true})
}
