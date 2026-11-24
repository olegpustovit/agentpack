package network

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

func Recent(home string) ([]session.SessionInfo, error) {
	store, err := state.Open(home)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return RecentIn(context.Background(), store.Reader)
}

// RecentIn reads recent launches from an open store; the gateway passes its own.
func RecentIn(ctx context.Context, db *sql.DB) ([]session.SessionInfo, error) {
	entries, err := session.QuerySessions(ctx, db, `SELECT `+session.SessionColumns+` FROM (
        SELECT *,row_number() OVER (PARTITION BY directory,harness,mode ORDER BY last_used DESC,id DESC) AS rank
        FROM sessions WHERE node_id=`+state.LocalNodeSQL+` AND hidden=0 AND transport<>'shell' AND directory<>''
    ) WHERE rank=1 ORDER BY last_used DESC,id DESC LIMIT 100`)
	for i := range entries {
		entries[i].ID = ""
		entries[i].State = session.TerminalState{}
	}
	return entries, err
}

type RecentFolder struct {
	session.SessionInfo
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	DetailsDeferred   bool   `json:"details_deferred,omitempty"`
}

func (folder *RecentFolder) CheckAvailability(explicit bool) {
	if !explicit && paths.RequiresUserAccess(folder.Directory) {
		// Keep the saved folder selectable. Actions still validate its current identity.
		folder.DetailsDeferred = true
		folder.Available = folder.FolderID != ""
		if !folder.Available {
			folder.UnavailableReason = "Open the folder to check availability"
		}
		return
	}
	identity, err := FolderIdentity(folder.Directory)
	switch {
	case err != nil:
		folder.UnavailableReason = "Folder is missing or inaccessible"
	case !SameFolder(folder.FolderID, identity):
		folder.UnavailableReason = "Folder changed since its last local launch"
	default:
		folder.Available = true
	}
}

func RecentFolders(recent []session.SessionInfo) []RecentFolder {
	folders := make([]RecentFolder, 0, len(recent))
	for _, entry := range recent {
		folder := RecentFolder{SessionInfo: entry}
		folder.CheckAvailability(false)
		folders = append(folders, folder)
	}
	return folders
}

func ForgetRecent(ctx context.Context, store *state.Store, info session.SessionInfo) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE sessions SET hidden=1 WHERE node_id=`+state.LocalNodeSQL+` AND directory=?
        AND (?='' OR harness=?) AND (?='' OR mode=?)`,
		filepath.Clean(info.Directory), info.Harness, info.Harness, info.Mode, info.Mode)
	return err
}

// SameFolder reports whether a recorded folder identity is the folder's current one.
func SameFolder(stored, current string) bool {
	return current != "" && stored == current
}
