package network

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

func (g *Gateway) runContentLocations(ctx context.Context, interval time.Duration) {
	if g.logs != nil {
		node, _ := g.auth.store.NodeID(ctx)
		ctx = obs.WithSink(ctx, g.logs, obs.BuildMetadata(g.options.Version, node, "gateway"))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		scanCtx, event := obs.Start(ctx, "cache.advertise", "verified package locations")
		if err := g.advertiseCacheLocations(scanCtx); err != nil && ctx.Err() == nil {
			event.Fail(err)
			event.End()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) advertiseCacheLocations(ctx context.Context) error {
	type location struct {
		hash           string
		size, verified int64
	}
	locations := map[string]location{}
	stamps, err := os.ReadDir(filepath.Join(g.options.Home, "verified"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range stamps {
		key, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !cache.ValidKey(key) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			continue
		}
		file, err := os.Open(filepath.Join(g.options.Home, "verified", entry.Name()))
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		if len(data) > 64<<10 {
			continue
		}
		var stamp struct {
			ContentHash string `json:"content_hash"`
			Metadata    string `json:"metadata"`
		}
		if json.Unmarshal(data, &stamp) != nil || !lockfile.ValidContentHash(stamp.ContentHash) || stamp.Metadata == "" {
			continue
		}
		root := filepath.Join(g.options.Home, "cache", key)
		rootInfo, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !rootInfo.IsDir() {
			continue
		}
		metadata, _, err := paths.TreeMetadata(root)
		if err != nil {
			return err
		}
		if metadata != stamp.Metadata {
			continue
		}
		var size int64
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				size += info.Size()
			}
			return nil
		}); err != nil {
			return err
		}
		locations[key] = location{stamp.ContentHash, size, info.ModTime().UnixNano()}
	}
	rows, err := g.auth.store.Reader.QueryContext(ctx, "SELECT cache_key FROM cache_locations WHERE node_id="+state.LocalNodeSQL)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		if _, ok := locations[key]; !ok {
			stale = append(stale, key)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	keys := append([]string(nil), stale...)
	for key := range locations {
		keys = append(keys, key)
	}
	// Bound each replicated version so a large cache inventory still fits a sync page.
	for batch := range slices.Chunk(keys, 64) {
		if err := state.Transaction(ctx, g.auth.store.DB, func(tx *sql.Tx) error {
			for _, key := range batch {
				item, ok := locations[key]
				if !ok {
					if _, err := tx.ExecContext(ctx, "DELETE FROM cache_locations WHERE cache_key=? AND node_id="+state.LocalNodeSQL, key); err != nil {
						return err
					}
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO cache_locations(cache_key,node_id,content_hash,size,verified_at)
 VALUES (?,`+state.LocalNodeSQL+`,?,?,?) ON CONFLICT(cache_key,node_id) DO UPDATE SET
 content_hash=excluded.content_hash,size=excluded.size,verified_at=excluded.verified_at
 WHERE content_hash<>excluded.content_hash OR size<>excluded.size OR verified_at<>excluded.verified_at`, key, item.hash, item.size, item.verified); err != nil {
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

func advertiseBlob(ctx context.Context, store *state.Store, id, sha string, size int64) error {
	if store == nil {
		return nil
	}
	if !strings.HasPrefix(id, "session-") || !validPeerSessionID(id) {
		return errors.New("invalid blob session")
	}
	_, err := store.DB.ExecContext(ctx, `INSERT INTO blob_locations(sha,node_id,session_id,size)
 VALUES (?,`+state.LocalNodeSQL+`,?,?) ON CONFLICT(sha,node_id) DO UPDATE SET session_id=excluded.session_id,size=excluded.size
 WHERE session_id<>excluded.session_id OR size<>excluded.size`, sha, id, size)
	return err
}
