package network

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// WithPeerCache adds an optional download source; it opens nothing on a cached launch.
func WithPeerCache(ctx context.Context, home string) context.Context {
	return cache.WithPeerRestore(ctx, func(ctx context.Context, pkg lockfile.Package, destination string) error {
		localHome := home
		if localHome == "" {
			var err error
			localHome, err = paths.UserAgentpackHome()
			if err != nil {
				return err
			}
		}
		return FetchCache(ctx, localHome, pkg, destination)
	})
}

func FetchCache(ctx context.Context, home string, pkg lockfile.Package, destination string) error {
	if !cache.ValidKey(pkg.CacheKey) || !lockfile.ValidContentHash(pkg.ContentHash) {
		return errors.New("peer fetch requires a verified cache key")
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".peer-cache-*.tar")
	if err != nil {
		return err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	path := "/api/cache/" + pkg.CacheKey + "?hash=" + url.QueryEscape(pkg.ContentHash)
	if err := NodeRequestStream(ctx, home, http.MethodGet, path, file, cache.MaxPeerTarBytes); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return cache.ExtractPeerTar(file, destination)
}

func (g *Gateway) serveCache(w http.ResponseWriter, r *http.Request) {
	g.serveCacheFrom(w, r, g.tailnetTarget)
}

func (g *Gateway) serveCacheFrom(w http.ResponseWriter, r *http.Request, targetFor func(context.Context, string) (*url.URL, error)) {
	key := r.PathValue("cache_key")
	if !cache.ValidKey(key) {
		http.Error(w, "invalid cache key", http.StatusBadRequest)
		return
	}
	peer, _ := r.Context().Value(replicaPeerKey{}).(string)
	actor, ok := actorFrom(r.Context())
	if peer != "" {
		if !g.meshPeer(r.Context(), peer) {
			http.Error(w, "cache transfer requires mesh enrollment", http.StatusForbidden)
			return
		}
	} else if !ok || actor.Kind != "local" {
		http.Error(w, "cache transfer requires local or peer transport", http.StatusForbidden)
		return
	}
	expected := r.URL.Query().Get("hash")
	if expected != "" && !lockfile.ValidContentHash(expected) {
		http.Error(w, "invalid content hash", http.StatusBadRequest)
		return
	}
	local, err := g.store.NodeID(r.Context())
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	var ownHash string
	err = g.store.Reader.QueryRowContext(r.Context(), "SELECT content_hash FROM cache_locations WHERE cache_key=? AND node_id=?", key, local).Scan(&ownHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		meshReadError(w, r, err)
		return
	}
	if err == nil && (expected == "" || expected == ownHash) {
		root := filepath.Join(g.options.Home, "cache", key)
		if info, err := os.Lstat(root); err == nil && info.IsDir() {
			actual, err := cache.TreeDigest(root)
			if err != nil {
				meshReadError(w, r, err)
				return
			}
			if actual == ownHash {
				w.Header().Set("Content-Type", "application/x-tar")
				if err := cache.WritePeerTar(root, w); err != nil {
					if event := obs.From(r.Context()); event != nil {
						event.Fail(err)
					}
				}
				return
			}
			if event := obs.From(r.Context()); event != nil {
				event.Fail(errors.New("cache tree differs from its verified content hash"))
			}
			http.Error(w, "cache integrity mismatch", http.StatusConflict)
			return
		}
	}
	if peer != "" || expected == "" {
		http.Error(w, "cache entry unavailable", http.StatusNotFound)
		return
	}
	rows, err := g.store.Reader.QueryContext(r.Context(), "SELECT node_id FROM cache_locations WHERE cache_key=? AND content_hash=? AND node_id<>? ORDER BY node_id", key, expected, local)
	if err != nil {
		meshReadError(w, r, err)
		return
	}
	var peers []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			meshReadError(w, r, err)
			return
		}
		peers = append(peers, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		meshReadError(w, r, err)
		return
	}
	for _, id := range peers {
		if !g.meshPeer(r.Context(), id) {
			continue
		}
		target, err := targetFor(r.Context(), id)
		if err != nil {
			continue
		}
		endpoint := target.JoinPath("/api/cache/" + key)
		endpoint.RawQuery = url.Values{"hash": {expected}}.Encode()
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint.String(), nil)
		if err != nil {
			continue
		}
		request.Header.Set("traceparent", obs.TraceParent(r.Context()))
		client := &http.Client{Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		w.Header().Set("Content-Type", "application/x-tar")
		_, err = io.Copy(w, io.LimitReader(response.Body, cache.MaxPeerTarBytes+1))
		response.Body.Close()
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
		return
	}
	http.Error(w, "cache entry unavailable", http.StatusNotFound)
}
