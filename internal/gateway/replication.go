package gateway

import (
	"context"
	"errors"
	"github.com/olegpustovit/agentpack/internal/network"
	"net/http"
	"time"
)

type replicaPeerKey struct{}

const replicationPrefix = "/api/library/replication/"

type replicaContact struct {
	Reachable   bool      `json:"reachable"`
	At          time.Time `json:"at"`
	LastSuccess time.Time `json:"last_sync,omitzero"`
	Error       string    `json:"error,omitempty"`
	NeedsUpdate bool      `json:"needs_update,omitempty"`
	Schema      int       `json:"schema_version,omitempty"`
	Version     int64     `json:"version,omitempty"`
	Lag         *int64    `json:"lag,omitempty"`
}

// enrollPeer pairs an allowed peer; an unclaimed gateway first closes its claim window to join the mesh's accounts.
func (g *Gateway) enrollPeer(ctx context.Context, peer string) error {
	var claimed bool
	err := g.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users) OR (SELECT join_peer<>'' FROM local_node WHERE id=1)").Scan(&claimed)
	if err == nil && !claimed {
		err = PrepareMeshJoin(ctx, g.store, peer)
	}
	if err == nil {
		err = g.options.Library.Store.EnrollPeer(peer)
	}
	if err == nil {
		g.emitEvent(eventMessage{Topic: "replication"})
	}
	return err
}

func (g *Gateway) registerRegistryReplication(mux routeRegistrar) {
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
		var input struct {
			Peer string `json:"peer"`
		}
		if !network.ReadJSON(w, r, &input) {
			return
		}
		config, err := ReadSharingConfig(g.store)
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
		err = g.enrollPeer(r.Context(), input.Peer)
		libraryResponse(w, map[string]bool{"enrolled": err == nil}, err)
	})
	mux.HandleFunc("DELETE "+replicationPrefix+"peers/{peer}", func(w http.ResponseWriter, r *http.Request) {
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
		if !network.ReadJSON(w, r, &input) {
			return
		}
		if !g.peer(input.Peer).Mesh {
			http.Error(w, "state peer is not enrolled for mesh sync", http.StatusBadRequest)
			return
		}
		g.wakeStatePeer(input.Peer)
		network.WriteJSON(w, map[string]bool{"synced": true})
	})
}
