package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

const syncChunkSize = syncPageLimit*3/4 - 2048 // JSON base64 and envelope must fit the page limit.

type stateTransfer struct {
	ID      string `json:"id"`
	Size    int64  `json:"size"`
	Hash    string `json:"sha256"`
	Version int64  `json:"version"`
}

type stateTransferFile struct {
	stateTransfer
	path    string
	info    os.FileInfo
	site    []byte
	expires time.Time
}

// One immutable snapshot per approved peer; retrying a pull replaces an unfinished transfer.
func (g *Gateway) newStateTransfer(ctx context.Context, peer string, version int64, exclude []byte) (_ *stateTransfer, result error) {
	g.syncTransferMu.Lock()
	defer g.syncTransferMu.Unlock()
	if prior := g.syncTransfers[peer]; prior != nil {
		if err := os.Remove(prior.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		delete(g.syncTransfers, peer)
	}
	// shortcut: a killed process can leave private OS temp files, scavenge them if interrupted transfers accumulate.
	file, err := os.CreateTemp("", "agentpack-sync-*")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			result = errors.Join(result, os.Remove(file.Name()))
		}
	}()
	hash := sha256.New()
	if err := g.store.Replicator().WriteVersion(ctx, version, exclude, io.MultiWriter(file, hash)); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	site, err := g.store.SiteID(ctx)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	transfer := &stateTransferFile{stateTransfer: stateTransfer{ID: network.RandomToken(), Size: info.Size(), Hash: hex.EncodeToString(hash.Sum(nil)), Version: version}, path: file.Name(), info: info, site: site, expires: time.Now().Add(2 * time.Minute)}
	if g.syncTransfers == nil {
		g.syncTransfers = make(map[string]*stateTransferFile)
	}
	g.syncTransfers[peer] = transfer
	keep = true
	return &transfer.stateTransfer, nil
}

func (g *Gateway) cleanStateTransfers(now time.Time) error {
	g.syncTransferMu.Lock()
	defer g.syncTransferMu.Unlock()
	var result error
	for peer, transfer := range g.syncTransfers {
		if !now.IsZero() && now.Before(transfer.expires) {
			continue
		}
		err := os.Remove(transfer.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
			continue
		}
		delete(g.syncTransfers, peer)
	}
	return result
}

func (g *Gateway) serveStateTransfer(w http.ResponseWriter, r *http.Request, peer string) {
	page, status, err := g.readStateTransfer(r, peer)
	if err != nil {
		if status >= 500 {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	network.WriteJSON(w, page)
}

func (g *Gateway) readStateTransfer(r *http.Request, peer string) (stateChangePage, int, error) {
	g.syncTransferMu.Lock()
	defer g.syncTransferMu.Unlock()
	var page stateChangePage
	transfer := g.syncTransfers[peer]
	if transfer == nil || transfer.ID != r.URL.Query().Get("transfer") || time.Now().After(transfer.expires) {
		return page, http.StatusNotFound, errors.New("state transfer expired or unknown")
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset >= transfer.Size {
		return page, http.StatusBadRequest, errors.New("invalid state transfer offset")
	}
	info, err := os.Lstat(transfer.path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, transfer.info) || info.Size() != transfer.Size {
		return page, http.StatusInternalServerError, errors.Join(err, errors.New("state transfer file changed"))
	}
	file, err := os.Open(transfer.path)
	if err != nil {
		return page, http.StatusInternalServerError, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return page, http.StatusInternalServerError, errors.Join(err, errors.New("state transfer changed while opening"))
	}
	data, err := io.ReadAll(io.NewSectionReader(file, offset, min(int64(syncChunkSize), transfer.Size-offset)))
	if err != nil {
		return page, http.StatusInternalServerError, err
	}
	page = stateChangePage{Schema: replicationVersion, SiteID: transfer.site, Next: transfer.Version, Transfer: &transfer.stateTransfer, Data: data, Offset: offset + int64(len(data))}
	return page, http.StatusOK, nil
}

func fetchStateTransfer(ctx context.Context, client *http.Client, target *url.URL, initial stateChangePage) (changes []state.Change, result error) {
	transfer := initial.Transfer
	hashBytes, err := hex.DecodeString(transfer.Hash)
	id, idErr := hex.DecodeString(transfer.ID)
	if err != nil || len(hashBytes) != sha256.Size || idErr != nil || len(id) != 32 || transfer.Size <= 0 || transfer.Version != initial.Next {
		return nil, errors.New("invalid state transfer descriptor")
	}
	file, err := os.CreateTemp("", "agentpack-sync-receive-*")
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, file.Close(), os.Remove(file.Name())) }()
	hash := sha256.New()
	out := io.MultiWriter(file, hash)
	for offset := int64(0); offset < transfer.Size; {
		endpoint := target.JoinPath("/api/sync/changes")
		endpoint.RawQuery = url.Values{"schema": {strconv.Itoa(replicationVersion)}, "transfer": {transfer.ID}, "offset": {strconv.FormatInt(offset, 10)}}.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("traceparent", obs.TraceParent(ctx))
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("state transfer peer is unreachable")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, syncPageLimit+1))
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("state transfer returned HTTP %d", response.StatusCode)
		}
		if readErr != nil || closeErr != nil || len(body) > syncPageLimit {
			return nil, errors.New("state transfer chunk is incomplete or too large")
		}
		var page stateChangePage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, errors.New("invalid state transfer chunk")
		}
		if page.Schema != initial.Schema || !bytes.Equal(page.SiteID, initial.SiteID) || page.Next != initial.Next || page.Transfer == nil || *page.Transfer != *transfer || len(page.Changes) != 0 || len(page.Data) == 0 || len(page.Data) > syncChunkSize || page.Offset != offset+int64(len(page.Data)) || page.Offset > transfer.Size {
			return nil, errors.New("inconsistent state transfer chunk")
		}
		if _, err := out.Write(page.Data); err != nil {
			return nil, err
		}
		offset = page.Offset
	}
	if !bytes.Equal(hash.Sum(nil), hashBytes) {
		return nil, errors.New("state transfer checksum mismatch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&changes); err != nil {
		return nil, errors.New("invalid state transfer changes")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing state transfer data")
	}
	for _, change := range changes {
		if change.DBVersion != transfer.Version {
			return nil, errors.New("state transfer contains another version")
		}
	}
	return changes, nil
}
