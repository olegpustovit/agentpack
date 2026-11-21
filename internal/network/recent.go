package network

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/state"
)

func Recent(home string) ([]SessionInfo, error) {
	store, err := state.Open(home)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return recentIn(context.Background(), store.Reader)
}

// recentIn reads recent launches from an open store; the gateway passes its own.
func recentIn(ctx context.Context, db *sql.DB) ([]SessionInfo, error) {
	entries, err := querySessions(ctx, db, `SELECT `+sessionColumns+` FROM (
        SELECT *,row_number() OVER (PARTITION BY directory,harness,mode ORDER BY last_used DESC,id DESC) AS rank
        FROM sessions WHERE node_id=`+state.LocalNodeSQL+` AND hidden=0 AND transport<>'shell' AND directory<>''
    ) WHERE rank=1 ORDER BY last_used DESC,id DESC LIMIT 100`)
	for i := range entries {
		entries[i].ID = ""
		entries[i].State = TerminalState{}
	}
	return entries, err
}

type RecentFolder struct {
	SessionInfo
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Availability is current filesystem state, never a persisted authorization grant.
func RecentFolders(home string) ([]RecentFolder, error) {
	recent, err := Recent(home)
	if err != nil {
		return nil, err
	}
	return recentFolders(recent), nil
}

func recentFolders(recent []SessionInfo) []RecentFolder {
	folders := make([]RecentFolder, 0, len(recent))
	for _, entry := range recent {
		folder := RecentFolder{SessionInfo: entry}
		identity, err := FolderIdentity(entry.Directory)
		switch {
		case err != nil:
			folder.UnavailableReason = "Folder is missing or inaccessible"
		case !sameFolder(entry.FolderID, identity):
			folder.UnavailableReason = "Folder changed since its last local launch"
		default:
			folder.Available = true
		}
		folders = append(folders, folder)
	}
	return folders
}

func ForgetRecent(home string, info SessionInfo) error {
	store, err := state.Open(home)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = store.DB.Exec(`UPDATE sessions SET hidden=1 WHERE node_id=`+state.LocalNodeSQL+` AND directory=?
        AND (?='' OR harness=?) AND (?='' OR mode=?)`,
		filepath.Clean(info.Directory), info.Harness, info.Harness, info.Mode, info.Mode)
	return err
}
