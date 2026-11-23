package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type archiveEdit struct {
	PK      []byte `json:"pk"`
	Session string `json:"session"`
	CL      int64  `json:"cl"`
	Version int64  `json:"version"`
	Site    []byte `json:"site"`
	Deleted bool   `json:"deleted"`
}

func eventKey(ctx context.Context, tx *sql.Tx, pk []byte) (string, int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT cell FROM crsql_unpack_columns(?)", pk)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	var id string
	var seq int64
	if !rows.Next() || rows.Scan(&id) != nil || !rows.Next() || rows.Scan(&seq) != nil || rows.Next() || id == "" || len(id) > 256 || seq <= 0 {
		return "", 0, errors.New("invalid archived event key")
	}
	return id, seq, rows.Err()
}

// Preserve edits in one journal transaction before a replicated tombstone can absorb them.
func (s *Store) preserveArchiveEdits(ctx context.Context, writer transactionStarter, changes []Change, values []any) error {
	var eventKeys []string
	clocks := map[string]string{}
	for i, change := range changes {
		switch {
		case change.Table == "conversation_events" && (change.Column == "body" || change.Column == "-1" && change.CL%2 == 0):
			eventKeys = append(eventKeys, hex.EncodeToString(change.PK))
		case change.Table == "archive_event_clocks" && change.Column == "clock":
			// The first prune can bring its proof in the same page.
			var pk []byte
			if err := s.Reader.QueryRowContext(ctx, "SELECT cell FROM crsql_unpack_columns(?)", change.PK).Scan(&pk); err != nil {
				return err
			}
			clocks[string(pk)], _ = values[i].(string)
		}
	}
	if len(eventKeys) == 0 {
		return nil
	}
	keys, err := json.Marshal(eventKeys)
	if err != nil {
		return err
	}
	rows, err := s.Reader.QueryContext(ctx, `SELECT a.pk,a.clock FROM json_each(?) k
 JOIN archive_event_clocks a ON a.pk=unhex(k.value)`, string(keys))
	if err != nil {
		return err
	}
	for rows.Next() {
		var pk []byte
		var clock string
		if err := rows.Scan(&pk, &clock); err != nil {
			rows.Close()
			return err
		}
		clocks[string(pk)] = clock
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(clocks) == 0 {
		return err
	}
	return Transaction(ctx, writer, func(tx *sql.Tx) error {
		for i, change := range changes {
			deleted := change.Column == "-1" && change.CL%2 == 0
			if change.Table != "conversation_events" || change.Column != "body" && !deleted {
				continue
			}
			encoded, ok := clocks[string(change.PK)]
			if !ok {
				continue
			}
			if err := func() error {
				id, seq, err := eventKey(ctx, tx, change.PK)
				if err != nil {
					return err
				}
				var previous archiveClock
				if err := json.Unmarshal([]byte(encoded), &previous); err != nil {
					return err
				}
				if previous.CL <= 0 || previous.Version < 0 || len(previous.Site) != 16 {
					return errors.New("invalid archived event clock")
				}
				body, _ := values[i].(string)
				clock := archiveClock{CL: change.CL, Version: change.ColVersion, Site: change.SiteID}
				if deleted && bytes.Equal(change.SiteID, previous.PrunedBy) {
					err := tx.QueryRowContext(ctx, `SELECT cl,col_version,site_id,val FROM crsql_changes
 WHERE "table"='conversation_events' AND pk=? AND cid='body'`, change.PK).Scan(&clock.CL, &clock.Version, &clock.Site, &body)
					if errors.Is(err, sql.ErrNoRows) {
						return nil
					}
					if err != nil {
						return err
					}
					deleted = false
				}
				if deleted {
					body = ""
				}
				if !archiveUpdatePending(clock, body, previous) {
					return nil
				}
				if !deleted {
					var entry struct {
						Seq int64 `json:"seq"`
					}
					if json.Unmarshal([]byte(body), &entry) != nil || entry.Seq != seq {
						return errors.New("invalid archived event body")
					}
				}
				edit := archiveEdit{PK: change.PK, Session: id, CL: clock.CL, Version: clock.Version, Site: clock.Site, Deleted: deleted}
				metadata, err := json.Marshal(edit)
				if err != nil {
					return err
				}
				key := archivedValueHash(string(metadata) + "\x00" + body)
				var acknowledged bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM crsql_changes
 WHERE "table"='archive_edits' AND pk=crsql_pack_columns(?) AND cid='-1' AND cl%2=0)`, key).Scan(&acknowledged); err != nil {
					return err
				}
				if acknowledged {
					return nil
				}
				_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO archive_edits(id,edit,body) VALUES (?,?,?)", key, string(metadata), body)
				return err
			}(); err != nil {
				return err
			}
		}
		return nil
	})
}

// DrainArchiveEdits acknowledges a relayed edit only after archive.db has durably committed it.
func (s *Store) DrainArchiveEdits(ctx context.Context) (result error) {
	local, err := s.NodeID(ctx)
	if err != nil {
		return err
	}
	roles, err := s.Main(ctx)
	if err != nil || roles.MainNode != local || roles.ArchiveNode != local {
		return err
	}
	rows, err := s.Reader.QueryContext(ctx, `SELECT a.id FROM archive_edits a
 JOIN sessions s ON s.id=json_extract(a.edit,'$.session') WHERE s.archived_at>0 ORDER BY a.id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(ids) == 0 {
		return err
	}
	if _, err := os.Lstat(filepath.Join(s.home, "archive.db")); err != nil {
		return err
	}
	archive, err := OpenArchive(s.home)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, archive.Close()) }()
	for _, id := range ids {
		if err := Transaction(ctx, s.DB, func(tx *sql.Tx) error {
			var ready bool
			if err := tx.QueryRowContext(ctx, "SELECT "+LocalArchiveSQL).Scan(&ready); err != nil {
				return err
			}
			if !ready {
				return ErrArchivePending
			}
			var encoded, body string
			if err := tx.QueryRowContext(ctx, "SELECT edit,body FROM archive_edits WHERE id=?", id).Scan(&encoded, &body); errors.Is(err, sql.ErrNoRows) {
				return nil
			} else if err != nil {
				return err
			}
			var edit archiveEdit
			if err := json.Unmarshal([]byte(encoded), &edit); err != nil {
				return err
			}
			if id != archivedValueHash(encoded+"\x00"+body) || edit.CL <= 0 || edit.Version < 0 || len(edit.Site) != 16 {
				return errors.New("invalid archived edit")
			}
			session, _, err := eventKey(ctx, tx, edit.PK)
			if err != nil {
				return err
			}
			if session != edit.Session || edit.Deleted != (edit.CL%2 == 0) {
				return errors.New("invalid archived edit key or lifetime")
			}
			if err := applyArchiveEdit(ctx, tx, archive, edit, body); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM archive_edits WHERE id=?", id)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
