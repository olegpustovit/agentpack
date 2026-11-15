package network

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

func archiveBlobRefs(body string) ([]string, error) {
	var entry struct {
		Kind    string `json:"kind"`
		Payload struct {
			Image *struct {
				Blob string `json:"blob"`
			} `json:"image"`
			Images []struct {
				Blob string `json:"blob"`
			} `json:"images"`
			Resource *struct {
				Blob string `json:"blob"`
			} `json:"resource_link"`
		} `json:"payload"`
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(body), &kind); err != nil {
		return nil, err
	}
	if kind.Kind != "prompt" && kind.Kind != "user_prompt" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		return nil, err
	}
	var blobs []string
	if entry.Payload.Image != nil {
		blobs = append(blobs, entry.Payload.Image.Blob)
	}
	for _, image := range entry.Payload.Images {
		blobs = append(blobs, image.Blob)
	}
	if entry.Payload.Resource != nil {
		blobs = append(blobs, entry.Payload.Resource.Blob)
	}
	for i, blob := range blobs {
		if !strings.HasPrefix(blob, "sha256:") {
			return nil, errors.New("invalid archived blob reference")
		}
		sha, err := validateBlobSHA(strings.TrimPrefix(blob, "sha256:"))
		if err != nil {
			return nil, err
		}
		blobs[i] = sha
	}
	slices.Sort(blobs)
	return slices.Compact(blobs), nil
}

func (g *Gateway) prepareArchiveBlobs(ctx context.Context, rows *sql.Rows, targetFor func(context.Context, string) (*url.URL, error)) error {
	pending := map[string]map[string]bool{}
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return err
		}
		refs, err := archiveBlobRefs(body)
		if err != nil {
			rows.Close()
			return err
		}
		for _, sha := range refs {
			if pending[id] == nil {
				pending[id] = map[string]bool{}
			}
			pending[id][sha] = true
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for id, refs := range pending {
		for sha := range refs {
			if _, err := g.fetchBlob(ctx, id, sha, targetFor); err != nil {
				return err
			}
			if _, err := g.auth.store.DB.ExecContext(ctx, "INSERT OR IGNORE INTO archive_blobs(session_id,sha) VALUES (?,?)", id, sha); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Gateway) prepareArchiveSession(ctx context.Context, id string, targetFor func(context.Context, string) (*url.URL, error)) error {
	rows, err := g.auth.store.Reader.QueryContext(ctx, "SELECT session_id,body FROM conversation_events WHERE session_id=?", id)
	if err != nil {
		return err
	}
	return g.prepareArchiveBlobs(ctx, rows, targetFor)
}

func verifyArchiveSessionBlobs(ctx context.Context, tx *sql.Tx, home, id string) error {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM conversation_events WHERE session_id=?", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return err
		}
		refs, err := archiveBlobRefs(body)
		if err != nil {
			return err
		}
		for _, sha := range refs {
			dir, err := sessionBlobDir(home, id)
			if err != nil {
				return err
			}
			if _, err := readSessionBlob(dir, sha); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO archive_blobs(session_id,sha) VALUES (?,?)", id, sha); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func (g *Gateway) drainArchiveEdits(ctx context.Context, targetFor func(context.Context, string) (*url.URL, error)) error {
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return err
	}
	roles, err := g.auth.store.Main(ctx)
	if err != nil || roles.MainNode != local || roles.ArchiveNode != local {
		return err
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT json_extract(a.edit,'$.session'),a.body FROM archive_edits a
 JOIN sessions s ON s.id=json_extract(a.edit,'$.session') WHERE s.archived_at>0 AND a.body<>''`)
	if err != nil {
		return err
	}
	if err := g.prepareArchiveBlobs(ctx, rows, targetFor); err != nil {
		return err
	}
	return g.auth.store.DrainArchiveEdits(ctx)
}

// PrepareArchive verifies copied attachments, fetching missing ones through the local gateway when it is running.
func PrepareArchive(ctx context.Context, home string) (result error) {
	if err := state.VerifyArchive(home); err != nil {
		return err
	}
	archive, err := state.ReadArchive(home)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, archive.Close()) }()
	store, err := state.Open(home)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	rows, err := archive.QueryContext(ctx, `SELECT session_id,body FROM conversation_events
 WHERE json_extract(body,'$.kind') IN ('prompt','user_prompt')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return err
		}
		refs, err := archiveBlobRefs(body)
		if err != nil {
			return err
		}
		for _, sha := range refs {
			dir, err := sessionBlobDir(home, id)
			if err != nil {
				return err
			}
			data, err := readSessionBlob(dir, sha)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			// The existing endpoint checks the SHA and stores the file before responding.
			if errors.Is(err, os.ErrNotExist) {
				if err := NodeRequestStream(ctx, home, http.MethodGet, "/api/sessions/"+id+"/blobs/"+sha, io.Discard, maxBlobBytes); err != nil {
					return fmt.Errorf("archive attachment unavailable; copy network/sessions/<session>/blobs too, or start both gateways to fetch it: %w", err)
				}
				data, err = readSessionBlob(dir, sha)
				if err != nil {
					return err
				}
			}
			if err := advertiseBlob(ctx, store, id, sha, int64(len(data))); err != nil {
				return err
			}
			if _, err := store.DB.ExecContext(ctx, "INSERT OR IGNORE INTO archive_blobs(session_id,sha) VALUES (?,?)", id, sha); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func (g *Gateway) cleanupArchivedBlobs(ctx context.Context, targetFor func(context.Context, string) (*url.URL, error)) error {
	roles, err := g.auth.store.Main(ctx)
	if err != nil {
		return err
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return err
	}
	if roles.MainNode == "" || roles.MainNode != roles.ArchiveNode || roles.MainNode == local {
		return nil
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, `SELECT s.id,s.archived_seq FROM sessions s
 LEFT JOIN archive_cleanup c ON c.session_id=s.id
 WHERE s.archived_at>0 AND s.archived_seq=s.last_seq AND (c.archived_seq IS NULL OR c.archived_seq<s.archived_seq)`)
	if err != nil {
		return err
	}
	type session struct {
		id  string
		seq int64
	}
	var sessions []session
	for rows.Next() {
		var s session
		if err := rows.Scan(&s.id, &s.seq); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, s := range sessions {
		dir, err := sessionBlobDir(g.options.Home, s.id)
		if err != nil {
			return err
		}
		refs, err := archiveSessionRefs(ctx, g.auth.store.Reader, s.id)
		if err != nil {
			return err
		}
		var localFiles bool
		if info, err := os.Lstat(filepath.Join(dir, "blobs")); err == nil {
			if !info.IsDir() {
				return errors.New("invalid archived blob directory")
			}
			localFiles = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, sha := range refs {
			if !localFiles {
				break
			}
			// A stale location alone is insufficient proof that Main still has the bytes.
			if !g.meshPeer(ctx, roles.MainNode) {
				return errors.New("archive Main is unavailable for attachment verification")
			}
			target, err := targetFor(ctx, roles.MainNode)
			if err != nil {
				return err
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath("/api/sessions/"+s.id+"/blobs/"+sha).String(), nil)
			if err != nil {
				return err
			}
			request.Header.Set("traceparent", obs.TraceParent(ctx))
			client := &http.Client{Transport: tailnetTransport, Timeout: workerRequestTimeout(http.MethodGet, "/blobs/"), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return errors.New("archive Main is unreachable for attachment verification")
			}
			data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBlobBytes+1))
			response.Body.Close()
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			if response.StatusCode != 200 || len(data) > maxBlobBytes || hex.EncodeToString(sum[:]) != sha {
				return errors.New("archive Main attachment copy could not be verified")
			}
		}
		if err := state.Transaction(ctx, g.auth.store.DB, func(tx *sql.Tx) error {
			var eligible bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND archived_at>0 AND archived_seq=? AND last_seq=archived_seq)
 AND coalesce((SELECT value FROM mesh_settings WHERE key='main_node'),'')=?
 AND coalesce((SELECT value FROM mesh_settings WHERE key='archive_node'),'')=?`, s.id, s.seq, roles.MainNode, roles.MainNode).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return nil
			}
			current, err := archiveSessionRefs(ctx, tx, s.id)
			if err != nil {
				return err
			}
			if !slices.Equal(current, refs) {
				return nil
			}
			if err := removeArchivedBlobFiles(dir); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM blob_locations WHERE node_id=? AND session_id=?", local, s.id); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO archive_cleanup(session_id,archived_seq) VALUES (?,?)
 ON CONFLICT(session_id) DO UPDATE SET archived_seq=excluded.archived_seq`, s.id, s.seq)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func archiveSessionRefs(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT sha FROM archive_blobs WHERE session_id=? ORDER BY sha", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		if valid, err := validateBlobSHA(sha); err != nil || valid != sha {
			return nil, errors.New("invalid archived blob hash")
		}
		refs = append(refs, sha)
	}
	return refs, rows.Err()
}

func removeArchivedBlobFiles(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lock := flock.New(dir + "/blobs.lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	files, err := fs.ReadDir(root.FS(), "blobs")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range files {
		if _, err := validateBlobSHA(file.Name()); err != nil || !file.Type().IsRegular() {
			return errors.New("unexpected file in archived blob directory")
		}
		if err := root.Remove("blobs/" + file.Name()); err != nil {
			return err
		}
	}
	return root.Remove("blobs")
}
