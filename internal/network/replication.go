package network

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
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/registry"
)

type replicaPeerKey struct{}

const replicationPrefix = "/api/library/replication/"

// shortcut: validate transfers in memory up to 64 MiB; page dependency batches if metadata outgrows this bound.
const replicaBatchLimit = 64 << 20

type replicaTransfer struct {
	Objects map[string][]byte `json:"objects"`
}

type replicaContact struct {
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

func replicationPeerRoute(method, path string) bool {
	switch path {
	case replicationPrefix + "inventory":
		return method == http.MethodGet
	case replicationPrefix + "objects", replicationPrefix + "ack":
		return method == http.MethodPost
	}
	id, found := strings.CutPrefix(path, replicationPrefix+"objects/")
	return found && method == http.MethodGet && replicaDigest(id)
}

func replicaDigest(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(id) == id
}

func (g *Gateway) enrolledPeer(id string) bool {
	if g.options.Library.Store == nil || id == "" {
		return false
	}
	peers, err := g.options.Library.Store.ListPeers()
	if err != nil {
		return false
	}
	for _, peer := range peers {
		if peer.Node == id {
			return true
		}
	}
	return false
}

func (g *Gateway) registerRegistryReplication(mux *http.ServeMux) {
	store := g.options.Library.Store
	if store == nil {
		return
	}
	// Only peerHandler can attach this identity, after Tailscale whois and policy checks.
	peerOnly := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			peer, _ := r.Context().Value(replicaPeerKey{}).(string)
			if !g.enrolledPeer(peer) {
				http.Error(w, "registry replication requires enrollment on both machines", http.StatusForbidden)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET "+replicationPrefix+"inventory", peerOnly(func(w http.ResponseWriter, r *http.Request) {
		value, err := store.Inventory()
		libraryResponse(w, value, err)
	}))
	mux.HandleFunc("GET "+replicationPrefix+"objects/{digest}", peerOnly(func(w http.ResponseWriter, r *http.Request) {
		value, err := store.ExportObject(r.PathValue("digest"))
		if err != nil {
			libraryResponse(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(value)
	}))
	mux.HandleFunc("POST "+replicationPrefix+"objects", peerOnly(func(w http.ResponseWriter, r *http.Request) {
		var input replicaTransfer
		if !readReplicaJSON(w, r, &input) {
			return
		}
		objects, err := checkedReplicaObjects(input)
		if err != nil {
			libraryResponse(w, nil, err)
			return
		}
		ids, err := store.ImportObjects(objects)
		// ImportObjects returns only after the objects and directory entries are durable.
		libraryResponse(w, ids, err)
	}))
	mux.HandleFunc("POST "+replicationPrefix+"ack", peerOnly(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Digests []string `json:"digests"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		peer := r.Context().Value(replicaPeerKey{}).(string)
		err := store.Acknowledge(peer, input.Digests)
		libraryResponse(w, map[string]bool{"acknowledged": err == nil}, err)
	}))
	mux.HandleFunc("GET "+replicationPrefix+"peers", func(w http.ResponseWriter, r *http.Request) {
		peers, err := store.ListPeers()
		contacts := map[string]replicaContact{}
		g.replicaStatus.Range(func(key, value any) bool { contacts[key.(string)] = value.(replicaContact); return true })
		libraryResponse(w, struct {
			Peers    any                       `json:"peers"`
			Contacts map[string]replicaContact `json:"contacts"`
		}{peers, contacts}, err)
	})
	mux.HandleFunc("POST "+replicationPrefix+"peers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Peer string `json:"peer"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		config, err := ReadSharingConfig(g.options.Home)
		if err != nil || !config.Enabled {
			libraryResponse(w, nil, errors.New("tailnet sharing is disabled"))
			return
		}
		status, err := tailnetSnapshot(r.Context())
		if err != nil {
			libraryResponse(w, nil, err)
			return
		}
		allowed := false
		for _, peer := range status.Peer {
			if peer != nil && peer.ID == input.Peer && allowedTailnetPeer(status.Self, peer, config) {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "choose an allowed Tailscale peer", http.StatusBadRequest)
			return
		}
		err = store.EnrollPeer(input.Peer)
		libraryResponse(w, map[string]bool{"enrolled": err == nil}, err)
	})
	mux.HandleFunc("DELETE "+replicationPrefix+"peers/{peer}", func(w http.ResponseWriter, r *http.Request) {
		err := store.RemovePeer(r.PathValue("peer"))
		g.replicaStatus.Delete(r.PathValue("peer"))
		libraryResponse(w, map[string]bool{"removed": err == nil}, err)
	})
	mux.HandleFunc("POST "+replicationPrefix+"sync", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Peer string `json:"peer"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		err := g.syncRegistryPeer(r.Context(), input.Peer)
		libraryResponse(w, map[string]bool{"synced": err == nil}, err)
	})
}

func readReplicaJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, replicaBatchLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid registry transfer", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid registry transfer", http.StatusBadRequest)
		return false
	}
	return true
}

func checkedReplicaObjects(input replicaTransfer) (map[string][]byte, error) {
	for id, data := range input.Objects {
		sum := sha256.Sum256(data)
		if !replicaDigest(id) || hex.EncodeToString(sum[:]) != id {
			return nil, errors.New("registry object digest mismatch")
		}
	}
	return input.Objects, nil
}

func registryPeerRequest(ctx context.Context, client *http.Client, target *url.URL, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if len(data) > replicaBatchLimit {
			return errors.New("registry transfer exceeds 64 MiB; reduce the retained revision history")
		}
		body = bytes.NewReader(data)
	}
	endpoint := *target
	endpoint.Path, endpoint.RawQuery = path, ""
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("registry peer is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("registry peer returned HTTP %d; verify enrollment on both machines", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, replicaBatchLimit+1))
	if err != nil || len(data) > replicaBatchLimit {
		return errors.New("registry peer response is incomplete or too large")
	}
	if raw, ok := output.(*[]byte); ok {
		*raw = data
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("registry peer returned invalid data")
	}
	return nil
}

func exchangeRegistry(ctx context.Context, store *registry.Store, peer string, target *url.URL, client *http.Client) error {
	var remote registry.Inventory
	if err := registryPeerRequest(ctx, client, target, "GET", replicationPrefix+"inventory", nil, &remote); err != nil {
		return err
	}
	if remote.Schema != 1 {
		return errors.New("unsupported registry inventory schema")
	}
	local, err := store.Inventory()
	if err != nil {
		return err
	}
	localIDs := append(slices.Clone(local.Objects), local.Tombstones...)
	remoteIDs := append(slices.Clone(remote.Objects), remote.Tombstones...)
	pull := replicaTransfer{Objects: map[string][]byte{}}
	total := 0
	for _, id := range remoteIDs {
		if !replicaDigest(id) {
			return errors.New("invalid registry inventory digest")
		}
		if slices.Contains(localIDs, id) {
			continue
		}
		var data []byte
		if err := registryPeerRequest(ctx, client, target, "GET", replicationPrefix+"objects/"+id, nil, &data); err != nil {
			return err
		}
		total += len(data)
		if total > replicaBatchLimit {
			return errors.New("registry transfer exceeds 64 MiB")
		}
		pull.Objects[id] = data
	}
	objects, err := checkedReplicaObjects(pull)
	if err != nil {
		return err
	}
	if _, err := store.ImportObjects(objects); err != nil {
		return err
	}
	var ack map[string]bool
	if err := registryPeerRequest(ctx, client, target, "POST", replicationPrefix+"ack", map[string]any{"digests": remoteIDs}, &ack); err != nil {
		return err
	}
	if !ack["acknowledged"] {
		return errors.New("registry peer did not confirm receipt")
	}
	// A received tombstone may retire local objects that must not be sent back.
	local, err = store.Inventory()
	if err != nil {
		return err
	}
	localIDs = append(slices.Clone(local.Objects), local.Tombstones...)
	push := replicaTransfer{Objects: map[string][]byte{}}
	for _, id := range localIDs {
		if slices.Contains(remoteIDs, id) {
			continue
		}
		data, err := store.ExportObject(id)
		if err != nil {
			return err
		}
		push.Objects[id] = data
	}
	var written []string
	if len(push.Objects) > 0 {
		if err := registryPeerRequest(ctx, client, target, "POST", replicationPrefix+"objects", push, &written); err != nil {
			return err
		}
		for _, id := range written {
			if _, exists := push.Objects[id]; !exists {
				return errors.New("registry peer acknowledged an unexpected object")
			}
		}
		for id := range push.Objects {
			if !slices.Contains(written, id) {
				return errors.New("registry peer did not acknowledge every object")
			}
		}
	}
	acknowledged := slices.Clone(written)
	for _, id := range remoteIDs {
		if slices.Contains(localIDs, id) {
			acknowledged = append(acknowledged, id)
		}
	}
	if err := store.Acknowledge(peer, acknowledged); err != nil {
		return err
	}
	_, err = store.CollectGarbage()
	return err
}

func (g *Gateway) syncRegistryPeer(ctx context.Context, peer string) (err error) {
	g.replicaMu.Lock()
	defer g.replicaMu.Unlock()
	if !g.enrolledPeer(peer) {
		return errors.New("registry peer is not enrolled")
	}
	defer func() {
		contact := replicaContact{At: time.Now().UTC()}
		if err != nil {
			contact.Error = err.Error()
		}
		g.replicaStatus.Store(peer, contact)
	}()
	target, err := tailnetTarget(ctx, g.options.Home, peer)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client := &http.Client{Transport: tailnetMutationTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return exchangeRegistry(ctx, g.options.Library.Store, peer, target, client)
}

func (g *Gateway) runRegistryReplication(ctx context.Context) {
	if g.options.Library.Store == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		peers, err := g.options.Library.Store.ListPeers()
		if err == nil {
			for _, peer := range peers {
				if ctx.Err() != nil {
					return
				}
				_ = g.syncRegistryPeer(ctx, peer.Node)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
