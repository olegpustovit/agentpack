package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type archiveClock struct {
	CL       int64  `json:"cl"`
	Version  int64  `json:"version"`
	Site     []byte `json:"site"`
	Hash     string `json:"hash"`
	PrunedBy []byte `json:"pruned_by"`
}

var ErrArchivePending = errors.New("newer archived history awaits the main gateway; retry after archive handover")

func archivedValueHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func archiveUpdatePending(clock archiveClock, body string, previous archiveClock) bool {
	if clock.CL != previous.CL {
		return clock.CL > previous.CL
	}
	if clock.Version != previous.Version {
		return clock.Version > previous.Version
	}
	return !bytes.Equal(clock.Site, previous.Site) || archivedValueHash(body) != previous.Hash
}

// RememberArchiveClocks retains original event clocks before archive pruning creates tombstones.
func RememberArchiveClocks(ctx context.Context, tx *sql.Tx, id string) error {
	rows, err := tx.QueryContext(ctx, `WITH keys AS MATERIALIZED (
 SELECT crsql_pack_columns(session_id,seq) AS pk FROM conversation_events WHERE session_id=?
 )
 SELECT c.pk,c.cl,c.col_version,c.site_id,c.val,crsql_site_id()
 FROM crsql_changes c CROSS JOIN keys k ON c.pk=k.pk
 WHERE c."table"='conversation_events' AND c.cid='body'`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	insert, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO archive_event_clocks(pk,clock) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	for rows.Next() {
		var pk []byte
		var clock archiveClock
		var value string
		if err := rows.Scan(&pk, &clock.CL, &clock.Version, &clock.Site, &value, &clock.PrunedBy); err != nil {
			return err
		}
		clock.Hash = archivedValueHash(value)
		body, err := json.Marshal(clock)
		if err != nil {
			return err
		}
		if _, err := insert.ExecContext(ctx, pk, string(body)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func guardArchiveMarkers(ctx context.Context, tx *sql.Tx, changes []Change, values []any) error {
	markers := map[string]bool{}
	for i, change := range changes {
		if change.Table == "sessions" && change.Column == "archived_at" {
			if value, ok := values[i].(int64); ok && value > 0 {
				var id string
				if err := tx.QueryRowContext(ctx, "SELECT cell FROM crsql_unpack_columns(?)", change.PK).Scan(&id); err != nil {
					return err
				}
				markers[id] = true
			}
		}
	}
	if len(markers) > 0 {
		for id := range markers {
			var pending bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND node_id=`+LocalNodeSQL+`
 AND ended_at=0 AND conversation_state IN ('working','needs_you'))`, id).Scan(&pending); err != nil {
				return err
			}
			if pending {
				return ErrArchivePending
			}
		}
	}
	return nil
}

func applyArchiveEdit(ctx context.Context, tx *sql.Tx, archive *sql.DB, edit archiveEdit, body string) error {
	id, seq, err := eventKey(ctx, tx, edit.PK)
	if err != nil {
		return err
	}
	var archived bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND archived_at>0)", id).Scan(&archived); err != nil {
		return err
	}
	if !archived {
		return ErrArchivePending
	}
	var previous archiveClock
	var encoded string
	err = tx.QueryRowContext(ctx, "SELECT clock FROM archive_event_clocks WHERE pk=?", edit.PK).Scan(&encoded)
	if err == nil {
		if err := json.Unmarshal([]byte(encoded), &previous); err != nil {
			return err
		}
		if previous.CL <= 0 || previous.Version < 0 || len(previous.Site) != 16 {
			return errors.New("invalid archived event clock")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if edit.Deleted && bytes.Equal(edit.Site, previous.PrunedBy) {
		return nil
	}
	if !edit.Deleted {
		var entry struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal([]byte(body), &entry) != nil || entry.Seq != seq {
			return errors.New("invalid archived event body")
		}
	}
	copy, err := archive.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer copy.Rollback()
	var oldBody string
	err = copy.QueryRowContext(ctx, "SELECT body FROM conversation_events WHERE session_id=? AND seq=?", id, seq).Scan(&oldBody)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var newer bool
	// Match cr-sqlite's lifetime/version/value/site ordering.
	if err := tx.QueryRowContext(ctx, "SELECT (?,?,?,?)>(?,?,?,?)", edit.CL, edit.Version, body, edit.Site, previous.CL, previous.Version, oldBody, previous.Site).Scan(&newer); err != nil {
		return err
	}
	if !newer {
		return nil
	}
	if edit.Deleted {
		_, err = copy.ExecContext(ctx, "DELETE FROM conversation_events WHERE session_id=? AND seq=?", id, seq)
	} else {
		_, err = copy.ExecContext(ctx, `INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)
 ON CONFLICT(session_id,seq) DO UPDATE SET body=excluded.body`, id, seq, body)
	}
	if err != nil {
		return err
	}
	incoming := archiveClock{CL: edit.CL, Version: edit.Version, Site: edit.Site, Hash: archivedValueHash(body), PrunedBy: previous.PrunedBy}
	if len(incoming.PrunedBy) == 0 {
		if err := tx.QueryRowContext(ctx, "SELECT crsql_site_id()").Scan(&incoming.PrunedBy); err != nil {
			return err
		}
	}
	clock, err := json.Marshal(incoming)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO archive_event_clocks(pk,clock) VALUES (?,?)
 ON CONFLICT(pk) DO UPDATE SET clock=excluded.clock`, edit.PK, string(clock)); err != nil {
		return err
	}
	return copy.Commit()
}
