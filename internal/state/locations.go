package state

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// Archive owns the replicated content directories (which machine holds which cache entry and
// chat image) and the archive bookkeeping rows outside sessions and conversation_events.
type Archive struct{ s *Store }

func (s *Store) Archive() Archive { return Archive{s} }

// CacheLocation is one verified cache entry on this machine.
type CacheLocation struct {
	Hash     string
	Size     int64
	Verified int64
}

// LocalCacheHash is the content hash this machine advertises for key, or "" when it advertises none.
func (a Archive) LocalCacheHash(ctx context.Context, key string) (string, error) {
	var hash string
	err := a.s.Reader.QueryRowContext(ctx, "SELECT content_hash FROM cache_locations WHERE cache_key=? AND node_id="+LocalNodeSQL, key).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

// CacheHolders lists other machines advertising key with exactly this content hash.
func (a Archive) CacheHolders(ctx context.Context, key, hash string) ([]string, error) {
	return queryStrings(ctx, a.s.Reader, "SELECT node_id FROM cache_locations WHERE cache_key=? AND content_hash=? AND node_id<>"+LocalNodeSQL+" ORDER BY node_id", key, hash)
}

// SetLocalCache makes this machine's advertised cache entries equal to entries, in transactions
// small enough that a large inventory still fits a sync page.
func (a Archive) SetLocalCache(ctx context.Context, entries map[string]CacheLocation) error {
	advertised, err := queryStrings(ctx, a.s.Reader, "SELECT cache_key FROM cache_locations WHERE node_id="+LocalNodeSQL)
	if err != nil {
		return err
	}
	keys := slices.DeleteFunc(advertised, func(key string) bool { _, ok := entries[key]; return ok })
	for key := range entries {
		keys = append(keys, key)
	}
	for batch := range slices.Chunk(keys, 64) {
		if err := Transaction(ctx, a.s.DB, func(tx *sql.Tx) error {
			for _, key := range batch {
				item, ok := entries[key]
				if !ok {
					if _, err := tx.ExecContext(ctx, "DELETE FROM cache_locations WHERE cache_key=? AND node_id="+LocalNodeSQL, key); err != nil {
						return err
					}
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO cache_locations(cache_key,node_id,content_hash,size,verified_at)
 VALUES (?,`+LocalNodeSQL+`,?,?,?) ON CONFLICT(cache_key,node_id) DO UPDATE SET
 content_hash=excluded.content_hash,size=excluded.size,verified_at=excluded.verified_at
 WHERE content_hash<>excluded.content_hash OR size<>excluded.size OR verified_at<>excluded.verified_at`, key, item.Hash, item.Size, item.Verified); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// AdvertiseBlob records that this machine holds image sha for session id.
func (a Archive) AdvertiseBlob(ctx context.Context, id, sha string, size int64) error {
	_, err := a.s.DB.ExecContext(ctx, `INSERT INTO blob_locations(sha,node_id,session_id,size)
 VALUES (?,`+LocalNodeSQL+`,?,?) ON CONFLICT(sha,node_id) DO UPDATE SET session_id=excluded.session_id,size=excluded.size
 WHERE session_id<>excluded.session_id OR size<>excluded.size`, sha, id, size)
	return err
}

// BlobHolder is a machine advertising an image, and the session it filed it under.
type BlobHolder struct{ Node, Session string }

// LocalBlobSession is the session this machine filed image sha under, or "".
func (a Archive) LocalBlobSession(ctx context.Context, sha string) (string, error) {
	var session string
	err := a.s.Reader.QueryRowContext(ctx, "SELECT session_id FROM blob_locations WHERE sha=? AND node_id="+LocalNodeSQL, sha).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return session, err
}

// BlobHolders lists other machines holding sha, those filing it under session first.
func (a Archive) BlobHolders(ctx context.Context, sha, session string) ([]BlobHolder, error) {
	rows, err := a.s.Reader.QueryContext(ctx, `SELECT node_id,session_id FROM blob_locations
 WHERE sha=? AND node_id<>`+LocalNodeSQL+` ORDER BY session_id=? DESC,node_id`, sha, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var holders []BlobHolder
	for rows.Next() {
		var holder BlobHolder
		if err := rows.Scan(&holder.Node, &holder.Session); err != nil {
			return nil, err
		}
		holders = append(holders, holder)
	}
	return holders, rows.Err()
}

// KeepBlob records that archived session id references image sha, so the archive keeps it.
func (a Archive) KeepBlob(ctx context.Context, id, sha string) error {
	_, err := a.s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO archive_blobs(session_id,sha) VALUES (?,?)", id, sha)
	return err
}

// NoteCleanup records that this machine removed session id's archived images through seq.
func (a Archive) NoteCleanup(ctx context.Context, id string, seq int64) error {
	_, err := a.s.DB.ExecContext(ctx, `INSERT INTO archive_cleanup(session_id,archived_seq) VALUES (?,?)
 ON CONFLICT(session_id) DO UPDATE SET archived_seq=excluded.archived_seq WHERE excluded.archived_seq>archived_seq`, id, seq)
	return err
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
