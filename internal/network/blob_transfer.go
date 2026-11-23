package network

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func sessionBlobDir(home, id string) (string, error) {
	if !strings.HasPrefix(id, "session-") || !validPeerSessionID(id) {
		return "", errors.New("invalid blob session")
	}
	return filepath.Join(home, "network", "sessions", id), nil
}

func (g *Gateway) fetchBlob(ctx context.Context, id, sha string, targetFor func(context.Context, string) (*url.URL, error)) ([]byte, error) {
	dir, err := sessionBlobDir(g.options.Home, id)
	if err != nil {
		return nil, err
	}
	sha, err = validateBlobSHA(sha)
	if err != nil {
		return nil, err
	}
	data, err := readSessionBlob(dir, sha)
	if err == nil {
		if err := advertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
			return nil, err
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	var ownSession string
	err = g.store.Reader.QueryRowContext(ctx, "SELECT session_id FROM blob_locations WHERE sha=? AND node_id=?", sha, local).Scan(&ownSession)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && ownSession != id {
		if ownDir, err := sessionBlobDir(g.options.Home, ownSession); err == nil {
			if data, err := readSessionBlob(ownDir, sha); err == nil {
				if _, err := storeSessionBlob(dir, data); err != nil {
					return nil, err
				}
				if err := advertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
					return nil, err
				}
				return data, nil
			}
		}
	}
	rows, err := g.store.Reader.QueryContext(ctx, `SELECT node_id,session_id FROM blob_locations
 WHERE sha=? AND node_id<>? ORDER BY session_id=? DESC,node_id`, sha, local, id)
	if err != nil {
		return nil, err
	}
	type location struct{ node, session string }
	var locations []location
	for rows.Next() {
		var item location
		if err := rows.Scan(&item.node, &item.session); err != nil {
			rows.Close()
			return nil, err
		}
		locations = append(locations, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	lastErr := error(os.ErrNotExist)
	for _, item := range locations {
		if !g.meshPeer(ctx, item.node) {
			continue
		}
		if _, err := sessionBlobDir(g.options.Home, item.session); err != nil {
			continue
		}
		target, err := targetFor(ctx, item.node)
		if err != nil {
			lastErr = err
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath("/api/sessions/"+item.session+"/blobs/"+sha).String(), nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("traceparent", obs.TraceParent(ctx))
		client := &http.Client{Timeout: 10 * time.Second, Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			lastErr = errors.New("blob peer unreachable")
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxBlobBytes+1))
		response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		sum := sha256.Sum256(data)
		if len(data) > maxBlobBytes || hex.EncodeToString(sum[:]) != sha {
			lastErr = errBlobIntegrity
			if event := obs.From(ctx); event != nil {
				event.Fail(lastErr)
			}
			continue
		}
		if _, err := storeSessionBlob(dir, data); err != nil {
			return nil, err
		}
		if err := advertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, lastErr
}

func (g *Gateway) serveBlob(w http.ResponseWriter, r *http.Request) {
	id, sha := r.PathValue("id"), r.PathValue("sha")
	dir, err := sessionBlobDir(g.options.Home, id)
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	sha, err = validateBlobSHA(sha)
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	var data []byte
	if peer, _ := r.Context().Value(replicaPeerKey{}).(string); peer != "" {
		if !g.meshPeer(r.Context(), peer) {
			http.Error(w, "blob transfer requires mesh enrollment", http.StatusForbidden)
			return
		}
		data, err = readSessionBlob(dir, sha)
	} else {
		data, err = g.fetchBlob(r.Context(), id, sha, g.tailnetTarget)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		if errors.Is(err, errBlobIntegrity) {
			status = http.StatusConflict
		}
		if event := obs.From(r.Context()); event != nil && status != http.StatusNotFound {
			event.Fail(err)
		}
		http.Error(w, "blob unavailable", status)
		return
	}
	writeSessionBlob(w, data)
}
