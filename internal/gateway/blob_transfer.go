package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

func sessionBlobDir(home, id string) (string, error) {
	if !strings.HasPrefix(id, "session-") || !network.ValidPeerSessionID(id) {
		return "", errors.New("invalid blob session")
	}
	return filepath.Join(home, "network", "sessions", id), nil
}

func (g *Gateway) fetchBlob(ctx context.Context, id, sha string, targetFor func(context.Context, string) (*url.URL, error)) ([]byte, error) {
	dir, err := sessionBlobDir(g.options.Home, id)
	if err != nil {
		return nil, err
	}
	sha, err = network.ValidateBlobSHA(sha)
	if err != nil {
		return nil, err
	}
	data, err := network.ReadSessionBlob(dir, sha)
	if err == nil {
		if err := network.AdvertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
			return nil, err
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ownSession, err := g.store.Archive().LocalBlobSession(ctx, sha)
	if err != nil {
		return nil, err
	}
	if ownSession != "" && ownSession != id {
		if ownDir, err := sessionBlobDir(g.options.Home, ownSession); err == nil {
			if data, err := network.ReadSessionBlob(ownDir, sha); err == nil {
				if _, err := network.StoreSessionBlob(dir, data); err != nil {
					return nil, err
				}
				if err := network.AdvertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
					return nil, err
				}
				return data, nil
			}
		}
	}
	locations, err := g.store.Archive().BlobHolders(ctx, sha, id)
	if err != nil {
		return nil, err
	}
	lastErr := error(os.ErrNotExist)
	for _, item := range locations {
		if !g.peer(item.Node).Mesh {
			continue
		}
		if _, err := sessionBlobDir(g.options.Home, item.Session); err != nil {
			continue
		}
		target, err := targetFor(ctx, item.Node)
		if err != nil {
			lastErr = err
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath("/api/sessions/"+item.Session+"/blobs/"+sha).String(), nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("traceparent", obs.TraceParent(ctx))
		client := &http.Client{Timeout: 10 * time.Second, Transport: mesh.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			lastErr = errors.New("blob peer unreachable")
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, network.MaxBlobBytes+1))
		response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		sum := sha256.Sum256(data)
		if len(data) > network.MaxBlobBytes || hex.EncodeToString(sum[:]) != sha {
			lastErr = network.ErrBlobIntegrity
			if event := obs.From(ctx); event != nil {
				event.Fail(lastErr)
			}
			continue
		}
		if _, err := network.StoreSessionBlob(dir, data); err != nil {
			return nil, err
		}
		if err := network.AdvertiseBlob(ctx, g.store, id, sha, int64(len(data))); err != nil {
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
	sha, err = network.ValidateBlobSHA(sha)
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	var data []byte
	if peer, _ := r.Context().Value(replicaPeerKey{}).(string); peer != "" {
		if !g.peer(peer).Mesh {
			http.Error(w, "blob transfer requires mesh enrollment", http.StatusForbidden)
			return
		}
		data, err = network.ReadSessionBlob(dir, sha)
	} else {
		data, err = g.fetchBlob(r.Context(), id, sha, g.tailnetTarget)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		if errors.Is(err, network.ErrBlobIntegrity) {
			status = http.StatusConflict
		}
		if event := obs.From(r.Context()); event != nil && status != http.StatusNotFound {
			event.Fail(err)
		}
		http.Error(w, "blob unavailable", status)
		return
	}
	network.WriteSessionBlob(w, data)
}

type blobHeldKey struct {
	path    string
	size    int64
	modTime time.Time
}

// blobHeld reports whether this node stores bytes matching sha for the session. A verified file is
// remembered while its size and modification time hold, so repeated checks do not rehash it.
func (g *Gateway) blobHeld(id, sha string) (bool, error) {
	dir, err := sessionBlobDir(g.options.Home, id)
	if err != nil {
		return false, nil
	}
	info, err := os.Lstat(filepath.Join(dir, "blobs", sha))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	key := blobHeldKey{filepath.Join(dir, "blobs", sha), info.Size(), info.ModTime()}
	if _, ok := g.heldBlobs.Load(key); ok {
		return true, nil
	}
	if _, err := network.ReadSessionBlob(dir, sha); errors.Is(err, os.ErrNotExist) || errors.Is(err, network.ErrBlobIntegrity) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	g.heldBlobs.Store(key, true)
	return true, nil
}

// serveBlobHeld answers a peer's attachment check without sending the bytes; it never fetches.
func (g *Gateway) serveBlobHeld(w http.ResponseWriter, r *http.Request) {
	sha, err := network.ValidateBlobSHA(r.PathValue("sha"))
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	held, err := g.blobHeld(r.PathValue("id"), sha)
	if err != nil {
		fail(w, r, err)
		return
	}
	network.WriteJSON(w, map[string]bool{"held": held})
}

func (g *Gateway) uploadConversationBlob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, ok := sessionRow(r.Context(), id)
	if ok && info.Transport == "acp" && info.ConversationState == "starting" && info.EndedAt.IsZero() {
		if !session.AuthorizeWrite(w, r, info) {
			return
		}
		dir, err := sessionBlobDir(g.options.Home, id)
		if err != nil {
			fail(w, r, err)
			return
		}
		network.UploadSessionBlob(w, r, dir, g.store)
		return
	}
	g.forwardToWorker("/blobs")(w, r)
}
