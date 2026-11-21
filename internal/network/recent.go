package network

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

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
		case !SameFolder(entry.FolderID, identity):
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

// legacyFolderSQL matches a Unix identity recorded as "dev:inode"; Windows identities hold two colons.
const legacyFolderSQL = `folder_id GLOB '*:*' AND folder_id NOT GLOB '*:*:*'`

// normalizeFolderIDs rewrites this node's "dev:inode" identities to the inode, because macOS
// renumbers devices at boot. Ownership bindings are keyed by identity, so each is copied to its
// new key first, and usage rows merge into the new folder.
func normalizeFolderIDs(ctx context.Context, store *state.Store) error {
	local := state.LocalNodeSQL
	inode := `substr(folder_id,instr(folder_id,':')+1)`
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT folder_id,harness,agent_session_id FROM sessions
			WHERE node_id=`+local+` AND transport='acp' AND agent_session_id<>'' AND `+legacyFolderSQL)
		if err != nil {
			return err
		}
		type binding struct{ folder, harness, session string }
		var bindings []binding
		for rows.Next() {
			var b binding
			if err := rows.Scan(&b.folder, &b.harness, &b.session); err != nil {
				rows.Close()
				return err
			}
			bindings = append(bindings, b)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, b := range bindings {
			folder := b.folder[strings.IndexByte(b.folder, ':')+1:]
			oldKey, _, err := acpOwnerKey(b.folder, b.harness, b.session)
			if err != nil {
				continue // an unknown harness never had a binding
			}
			newKey, _, err := acpOwnerKey(folder, b.harness, b.session)
			if err != nil {
				return err
			}
			oldID, err := ownerBindingID(ctx, tx, oldKey)
			if err != nil {
				return err
			}
			newID, err := ownerBindingID(ctx, tx, newKey)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO sessions(id,node_id,folder_id,harness,started_by,owner_email,hidden,ended_at)
				SELECT ?,node_id,?,harness,started_by,owner_email,1,ended_at FROM sessions WHERE id=? AND node_id=`+local, newID, folder, oldID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET folder_id=`+inode+` WHERE node_id=`+local+` AND `+legacyFolderSQL); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_hourly(hour,node_id,harness,folder_id,model,turns,sessions,input_tokens,output_tokens,
				cache_read_tokens,cache_write_tokens,cost_micros,agent_ms)
			SELECT hour,node_id,harness,`+inode+`,model,sum(turns),sum(sessions),sum(input_tokens),sum(output_tokens),
				sum(cache_read_tokens),sum(cache_write_tokens),sum(cost_micros),sum(agent_ms)
			FROM usage_hourly WHERE node_id=`+local+` AND `+legacyFolderSQL+` GROUP BY 1,2,3,4,5
			ON CONFLICT(hour,node_id,harness,folder_id,model) DO UPDATE SET turns=turns+excluded.turns,sessions=sessions+excluded.sessions,
				input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
				cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens,cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens,
				cost_micros=cost_micros+excluded.cost_micros,agent_ms=agent_ms+excluded.agent_ms`); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM usage_hourly WHERE node_id=`+local+` AND `+legacyFolderSQL)
		return err
	})
}
