package network

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

const archiveAge = 7 * 24 * time.Hour

func (g *Gateway) runArchive(ctx context.Context, interval time.Duration, now func() time.Time) {
	if g.logs != nil {
		node, _ := g.store.NodeID(ctx)
		ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, node, "gateway"))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		passCtx, event := obs.Start(ctx, "archive.move", "inactive conversations")
		count, err := g.archivePass(passCtx, now())
		if err != nil && ctx.Err() == nil {
			event.Fail(err)
		}
		if err != nil || count > 0 {
			event.Set("count", count)
			event.End()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-g.archiveWake:
		}
	}
}

// wakeArchive runs the archive pass early after a pull changed state, so relayed edits drain and
// archived attachments clean up once, not once per peer worker.
func (g *Gateway) wakeArchive() {
	select {
	case g.archiveWake <- struct{}{}:
	default:
	}
}

func (g *Gateway) archivePass(ctx context.Context, now time.Time) (count int, result error) {
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return 0, err
	}
	roles, err := g.store.Main(ctx)
	if err != nil {
		return 0, err
	}
	if roles.MainNode != local || roles.ArchiveNode != local {
		return 0, g.cleanupArchivedBlobs(ctx, g.tailnetTarget)
	}
	if err := g.drainArchiveEdits(ctx, g.tailnetTarget); err != nil {
		return 0, err
	}
	rows, err := g.store.Reader.QueryContext(ctx, `SELECT id FROM sessions
 WHERE transport='acp' AND NOT (ended_at=0 AND conversation_state IN ('working','needs_you')) AND
 ((archived_at=0 AND last_event_at>0 AND last_event_at<?) OR
 (archived_at>0 AND EXISTS(SELECT 1 FROM conversation_events WHERE session_id=sessions.id)))
 ORDER BY last_event_at,id`, now.Add(-archiveAge).UnixNano())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(ids) == 0 {
		return 0, err
	}
	if _, err := os.Lstat(filepath.Join(g.options.Home, "archive.db")); errors.Is(err, os.ErrNotExist) {
		var archived bool
		if err := g.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE archived_at>0)").Scan(&archived); err != nil {
			return 0, err
		}
		if archived {
			return 0, errors.New("archive.db is missing; restore it before moving more history")
		}
	}
	archive, err := state.OpenArchive(g.options.Home)
	if err != nil {
		return 0, err
	}
	defer func() { result = errors.Join(result, archive.Close()) }()
	for _, id := range ids {
		if err := g.prepareArchiveSession(ctx, id, g.tailnetTarget); err != nil {
			_, event := obs.Start(ctx, "archive.blobs", "preserve attachments")
			event.Set("session_id", id)
			event.Fail(err)
			event.End()
			continue
		}
		moved, err := g.archiveSession(ctx, archive, id, now)
		if err != nil {
			return count, fmt.Errorf("archive session %s: %w", id, err)
		}
		if moved {
			count++
		}
	}
	if count > 0 {
		g.emitEvent(eventMessage{Topic: "sessions"})
	}
	return count, nil
}

const archiveStateQuery = `SELECT last_seq,archived_at FROM sessions WHERE id=?
 AND (archived_at>0 OR (last_event_at>0 AND last_event_at<?)) AND transport='acp' AND hidden=0
 AND NOT (ended_at=0 AND conversation_state IN ('working','needs_you'))
 AND coalesce((SELECT value FROM mesh_settings WHERE key='main_node'),'')=` + state.LocalNodeSQL + `
 AND coalesce((SELECT value FROM mesh_settings WHERE key='archive_node'),'')=` + state.LocalNodeSQL

// The pinned clock tables have indexed event keys. Joining through crsql_changes scans it for each event.
const archiveProgressQuery = `SELECT count(*),coalesce(max(seq),0),coalesce((
 SELECT max(c.db_version)
 FROM conversation_events__crsql_pks p
 CROSS JOIN conversation_events e ON e.session_id=p.session_id AND e.seq=p.seq
 CROSS JOIN conversation_events__crsql_clock c ON c.key=p.__crsql_key AND c.col_name='body'
 WHERE p.session_id=?1),0)
 FROM conversation_events WHERE session_id=?1`

func (g *Gateway) archiveSession(ctx context.Context, archive *sql.DB, id string, now time.Time) (bool, error) {
	g.archiveMu.Lock()
	defer g.archiveMu.Unlock()
	store := g.store
	snapshot, err := store.Reader.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer snapshot.Rollback()
	oldEnough := now.Add(-archiveAge).UnixNano()
	var lastSeq, archivedAt int64
	err = snapshot.QueryRowContext(ctx, archiveStateQuery, id, oldEnough).Scan(&lastSeq, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && lastSeq <= 0 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var count, liveHighest, version int64
	if err := snapshot.QueryRowContext(ctx, archiveProgressQuery, id).Scan(&count, &liveHighest, &version); err != nil || count == 0 {
		return false, err
	}
	highest := liveHighest
	if archivedAt != 0 {
		var savedHighest int64
		if err := archive.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM conversation_events WHERE session_id=?", id).Scan(&savedHighest); err != nil {
			return false, err
		}
		if savedHighest == 0 {
			return false, errors.New("original archived history is missing; replicated history was kept")
		}
		highest = max(highest, savedHighest)
	}
	if highest != lastSeq {
		return false, nil
	}
	refs := map[string]struct{}{}
	if err := state.Transaction(ctx, archive, func(copy *sql.Tx) error {
		return g.copyArchiveSession(ctx, snapshot, copy, id, archivedAt, count, refs)
	}); err != nil {
		return false, err
	}
	if err := snapshot.Commit(); err != nil {
		return false, err
	}

	moved := false
	err = state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		var currentSeq, currentArchived int64
		err := tx.QueryRowContext(ctx, archiveStateQuery, id, oldEnough).Scan(&currentSeq, &currentArchived)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (currentSeq != lastSeq || currentArchived != archivedAt) {
			return nil
		}
		if err != nil {
			return err
		}
		var currentCount, currentHighest, currentVersion int64
		if err := tx.QueryRowContext(ctx, archiveProgressQuery, id).Scan(&currentCount, &currentHighest, &currentVersion); err != nil {
			return err
		}
		if currentCount != count || currentHighest != liveHighest || currentVersion != version {
			return nil
		}
		for sha := range refs {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO archive_blobs(session_id,sha) VALUES (?,?)", id, sha); err != nil {
				return err
			}
		}
		if err := state.RememberArchiveClocks(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET archived_seq=? WHERE id=? AND archived_seq<>?", lastSeq, id, lastSeq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_events WHERE session_id=?", id); err != nil {
			return err
		}
		if archivedAt == 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE sessions SET archived_at=? WHERE id=?", now.UnixNano(), id); err != nil {
				return err
			}
		}
		moved = true
		return nil
	})
	return moved && err == nil, err
}

func (g *Gateway) copyArchiveSession(ctx context.Context, snapshot, copy *sql.Tx, id string, archivedAt, expected int64, refs map[string]struct{}) error {
	if archivedAt == 0 {
		// Refresh a crash-recovery copy if events were edited or removed before the retry.
		if _, err := copy.ExecContext(ctx, "DELETE FROM conversation_events WHERE session_id=?", id); err != nil {
			return err
		}
	} else if err := copy.QueryRowContext(ctx, "SELECT count(*) FROM conversation_events WHERE session_id=?", id).Scan(&expected); err != nil {
		return err
	}
	insert, err := copy.PrepareContext(ctx, `INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)
 ON CONFLICT(session_id,seq) DO UPDATE SET body=excluded.body WHERE body<>excluded.body`)
	if err != nil {
		return err
	}
	defer insert.Close()
	rows, err := snapshot.QueryContext(ctx, "SELECT seq,body FROM conversation_events WHERE session_id=? ORDER BY seq", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	dir, err := sessionBlobDir(g.options.Home, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int64
		var body string
		if err := rows.Scan(&seq, &body); err != nil {
			return err
		}
		attachments, err := archiveBlobRefs(body)
		if err != nil {
			return err
		}
		for _, sha := range attachments {
			if _, ok := refs[sha]; !ok {
				if _, err := readSessionBlob(dir, sha); err != nil {
					return err
				}
				refs[sha] = struct{}{}
			}
		}
		if archivedAt != 0 {
			var exists bool
			if err := copy.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM conversation_events WHERE session_id=? AND seq=?)", id, seq).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				expected++
			}
		}
		if _, err := insert.ExecContext(ctx, id, seq, body); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var copied int64
	if err := copy.QueryRowContext(ctx, "SELECT count(*) FROM conversation_events WHERE session_id=?", id).Scan(&copied); err != nil {
		return err
	}
	if copied != expected {
		return errors.New("archive row count differs; replicated history was kept")
	}
	return nil
}
