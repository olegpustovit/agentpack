package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
		node, _ := g.store.NodeID(ctx)
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
	locations := map[string]state.CacheLocation{}
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
		locations[key] = state.CacheLocation{Hash: stamp.ContentHash, Size: size, Verified: info.ModTime().UnixNano()}
	}
	return g.store.Archive().SetLocalCache(ctx, locations)
}

func advertiseBlob(ctx context.Context, store *state.Store, id, sha string, size int64) error {
	if store == nil {
		return nil
	}
	if !strings.HasPrefix(id, "session-") || !validPeerSessionID(id) {
		return errors.New("invalid blob session")
	}
	return store.Archive().AdvertiseBlob(ctx, id, sha, size)
}
