package network

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type replicaPeerKey struct{}

const replicationPrefix = "/api/library/replication/"

type replicaContact struct {
	At          time.Time `json:"at"`
	LastSuccess time.Time `json:"last_sync,omitzero"`
	Error       string    `json:"error,omitempty"`
	NeedsUpdate bool      `json:"needs_update,omitempty"`
	Schema      int       `json:"schema_version,omitempty"`
	Version     int64     `json:"version,omitempty"`
	Lag         *int64    `json:"lag,omitempty"`
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
		if !g.meshOwner(w, r) {
			return
		}
		var input struct {
			Peer string `json:"peer"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		config, err := ReadSharingConfig(g.store, g.options.Home)
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
		var hasAccounts bool
		err = g.auth.store.Reader.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM users)").Scan(&hasAccounts)
		if err == nil && !hasAccounts {
			err = PrepareMeshJoin(r.Context(), g.auth.store, input.Peer)
		}
		if err == nil {
			err = store.EnrollPeer(input.Peer)
		}
		if err == nil {
			g.emitEvent(eventMessage{Topic: "replication"})
		}
		libraryResponse(w, map[string]bool{"enrolled": err == nil}, err)
	})
	mux.HandleFunc("DELETE "+replicationPrefix+"peers/{peer}", func(w http.ResponseWriter, r *http.Request) {
		if !g.meshOwner(w, r) {
			return
		}
		err := store.RemovePeer(r.PathValue("peer"))
		if err == nil {
			g.emitEvent(eventMessage{Topic: "replication"})
		}
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
		g.wakeStatePeer(input.Peer)
		g.emitEvent(eventMessage{Topic: "replication"})
		err := g.syncStatePeer(r.Context(), input.Peer)
		libraryResponse(w, map[string]bool{"synced": err == nil}, err)
	})
}

func (g *Gateway) syncStatePeer(ctx context.Context, peer string) error {
	if !g.meshPeer(ctx, peer) {
		return errors.New("state peer is not enrolled for mesh sync")
	}
	target, err := g.tailnetTarget(ctx, peer)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client := &http.Client{Transport: tailnetTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	_, _, err = pullState(ctx, g.auth.store, g.options.Library.Store, client, target)
	return err
}
