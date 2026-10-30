package network

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
)

// topology is the machine map. It never carries Tailscale IPs or current socket addresses.
type topology struct {
	Self  topologySelf   `json:"self"`
	Peers []topologyPeer `json:"peers"`
}
type topologySelf struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OS    string `json:"os"`
	Relay string `json:"relay"`
}
type topologyPeer struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OS         string    `json:"os"`
	State      string    `json:"state"`
	Connection string    `json:"connection"`
	Relay      string    `json:"relay"`
	RTTms      int64     `json:"rtt_ms,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitzero"`
	// Sessions is always 0 here; the UI fills it from the sessions query.
	Sessions int `json:"sessions"`
}

func tailnetTopology(ctx context.Context, home string) (topology, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	config, err := ReadSharingConfig(home)
	if err != nil {
		return topology{}, err
	}
	status, err := tailnetSnapshot(ctx)
	if err != nil {
		return topology{}, err
	}
	return topologyFrom(ctx, status, config, probeTailnetPeer)
}

// topologyFrom classifies every peer. Only online, permitted peers with an address are probed.
func topologyFrom(ctx context.Context, status tailnetStatus, config SharingConfig, probe probeFunc) (topology, error) {
	permitted := func(peer *tailnetPeer) bool {
		return config.Enabled && allowedTailnetPeer(status.Self, peer, config)
	}
	var probeable []*tailnetPeer
	for _, peer := range status.Peer {
		if peer != nil && peer.Online && permitted(peer) && tailnetAddress(peer) != "" {
			probeable = append(probeable, peer)
		}
	}
	probed, err := probeTailnetPeers(ctx, probeable, probe)
	if err != nil {
		return topology{}, err
	}
	result := topology{
		Self:  topologySelf{ID: status.Self.ID, Name: status.Self.HostName, OS: status.Self.OS, Relay: status.Self.Relay},
		Peers: []topologyPeer{},
	}
	for _, peer := range status.Peer {
		if peer == nil {
			continue
		}
		entry := topologyPeer{ID: peer.ID, Name: peer.HostName, OS: peer.OS, Relay: peer.Relay, Connection: connectionOf(peer), LastSeen: peer.LastSeen}
		switch hit, ok := probed[peer.ID]; {
		case !peer.Online:
			entry.State = "offline"
		case !permitted(peer):
			entry.State = "not-allowed"
		case ok:
			entry.State = "reachable"
			entry.RTTms = hit.RTT.Milliseconds()
		default:
			entry.State = "no-gateway"
		}
		result.Peers = append(result.Peers, entry)
	}
	slices.SortFunc(result.Peers, func(a, b topologyPeer) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return result, nil
}

// connectionOf names the path type without exposing the address behind it.
func connectionOf(peer *tailnetPeer) string {
	if peer.CurAddr != "" {
		return "direct"
	}
	if peer.Relay != "" {
		return "relay"
	}
	return ""
}
