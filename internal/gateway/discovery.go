package gateway

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

// The gateway is the mesh.Host its tailnet listener and proxy serve.
var _ mesh.Host = (*Gateway)(nil)

func (g *Gateway) PeerRoute(method, path string) bool { return peerRoute(method, path) }
func (g *Gateway) MeshPeer(id string) bool            { return g.peer(id).Mesh }
func (g *Gateway) Done() <-chan struct{}              { return g.done }

func (g *Gateway) Authenticated(r *http.Request) func() bool {
	private := g.localRequest(r)
	return func() bool { return private || g.auth.authenticated(r) }
}

// ServePeer answers /peer, then serves an enrolled peer's request as its forwarded account.
func (g *Gateway) ServePeer(w http.ResponseWriter, r *http.Request, self *mesh.Peer, identity mesh.Identity) {
	row, _ := routeFor(r.Method, r.URL.Path)
	if r.URL.Path == "/peer" {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		network.WriteJSON(w, mesh.Node{ID: self.ID, Name: self.HostName, Online: true, Version: g.options.Version, Schema: replicationVersion})
		return
	}
	peer := identity.Node.StableID
	if g.peer(peer).Node == "" {
		http.Error(w, "peer is not enrolled", http.StatusForbidden)
		return
	}
	removed, err := g.options.Library.Store.PeerRemoved(peer)
	if err != nil || removed {
		if err != nil {
			if event := obs.From(r.Context()); event != nil {
				event.Fail(err)
			}
		}
		http.Error(w, "peer is not enrolled", http.StatusForbidden)
		return
	}
	actor := session.Actor{Kind: "peer", User: identity.UserProfile.LoginName, Node: identity.Node.ComputedName, StableNodeID: identity.Node.StableID}
	actor, err = g.forwardedAccount(r.Context(), actor, r.Header.Get("X-Agentpack-User"))
	if err != nil {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
		http.Error(w, "forwarded account refused", http.StatusForbidden)
		return
	}

	// Read-only RPCs and machine-to-machine endpoints use the verified peer identity.
	if row.peer == peerWrite && !actor.Account {
		http.Error(w, "forwarded account required", http.StatusForbidden)
		return
	}
	actor.ForwardedBrowser = actor.Account && r.Header.Get("X-Agentpack-Browser") == "1"
	request := r.Clone(withCaller(session.WithActor(context.WithValue(r.Context(), replicaPeerKey{}, identity.Node.StableID), actor), callerPeer))
	request.Header = r.Header.Clone()
	request.Header.Del("X-Agentpack-User")
	request.Header.Del("X-Agentpack-Browser")
	request.Header.Del("Cookie")
	request.Header.Set("Authorization", "Bearer "+g.local.Secret)
	request.Host = g.origin.Host
	g.Handler().ServeHTTP(w, request)
}

// autoEnroll pairs every discovered peer the owner has not removed, so starting ap on an allowed tailnet machine is enough to be visible.
func (g *Gateway) autoEnroll(ctx context.Context, discovered []mesh.Node) {
	if g.options.Library.Store == nil {
		return
	}
	for _, node := range discovered {
		if g.peer(node.ID).Mesh {
			continue
		}
		removed, err := g.options.Library.Store.PeerRemoved(node.ID)
		if err == nil && removed {
			continue
		}
		if err == nil {
			err = g.enrollPeer(ctx, node.ID)
		}
		if err != nil {
			_, event := obs.Start(ctx, "mesh", "auto_enroll")
			event.Fail(err)
			event.End()
		}
	}
}

// tailnetNodes probes only allowed, online peers learned from the local daemon.
func (g *Gateway) tailnetNodes(ctx context.Context) ([]mesh.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	config, err := mesh.ReadSharingConfig(g.store)
	if err != nil {
		return nil, err
	}
	discovered, discoveryErr := mesh.Discover(ctx, config)
	g.autoEnroll(ctx, discovered)
	known, err := g.meshNodes(ctx, discovered)
	if err != nil {
		return nil, err
	}
	if len(known) == 0 && discoveryErr != nil {
		return nil, discoveryErr
	}
	return known, nil
}

func (g *Gateway) meshNodes(ctx context.Context, discovered []mesh.Node) ([]mesh.Node, error) {
	local, err := g.store.NodeID(ctx)
	if err != nil {
		return nil, err
	}
	nodes := map[string]mesh.Node{}
	for _, node := range discovered {
		nodes[node.ID] = node
	}
	recorded, err := g.store.Mesh().Nodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range recorded {
		if row.ID == local {
			continue
		}
		node := mesh.Node{ID: row.ID, Name: row.Name, Version: row.AppVersion, Schema: row.Schema}
		if node.Name == "" {
			node.Name = node.ID
		}
		if live, ok := nodes[node.ID]; ok {
			node.Name = live.Name
			node.Online = live.Online
			if live.Version != "" {
				node.Version = live.Version
			}
			if live.Schema != 0 {
				node.Schema = live.Schema
			}
		}
		if value, ok := g.replicaStatus.Load(node.ID); ok {
			contact := value.(replicaContact)
			if contact.Schema != 0 {
				node.Schema = contact.Schema
			}
		}
		nodes[node.ID] = node
	}
	if store := g.options.Library.Store; store != nil {
		peers, err := store.ListPeers()
		if err != nil {
			return nil, err
		}
		for _, peer := range peers {
			node, ok := nodes[peer.Node]
			if !ok && !peer.Mesh {
				// An unpaired legacy Library peer is not a machine until discovery finds it or it is paired.
				continue
			}
			if !ok {
				node = mesh.Node{ID: peer.Node, Name: peer.Node}
			}

			node.NeedsUpdate = !peer.Mesh
			if value, ok := g.replicaStatus.Load(peer.Node); ok {
				node.NeedsUpdate = node.NeedsUpdate || value.(replicaContact).NeedsUpdate
			}
			nodes[peer.Node] = node
		}
	}
	for _, node := range nodes {
		if node.Name == node.ID {
			// Legacy Library peers store only the stable ID; name them from the local daemon when it answers.
			if status, err := mesh.Snapshot(ctx); err == nil {
				mesh.NameFromTailnet(nodes, status)
			}
			break
		}
	}
	roles, err := g.store.Main(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]mesh.Node, 0, len(nodes))
	for _, node := range nodes {
		reachable, _ := g.nodeContact(node.ID, local)
		node.Online = node.Online || reachable
		node.Main = node.ID == roles.MainNode
		result = append(result, node)
	}
	slices.SortFunc(result, func(a, b mesh.Node) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// tailnetTarget resolves an online, allowed peer under this gateway's sharing settings.
func (g *Gateway) tailnetTarget(ctx context.Context, id string) (*url.URL, error) {
	return g.tailnet.Target(ctx, g.store, id)
}
