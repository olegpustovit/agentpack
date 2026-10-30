package network

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// topologyFixture has one peer per state. Probes are only allowed for "reach" and "relayed".
func topologyFixture() tailnetStatus {
	seen := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return tailnetStatus{
		Self: &tailnetPeer{ID: "self", HostName: "laptop", OS: "linux", Relay: "fra", UserID: 7},
		Peer: map[string]*tailnetPeer{
			"reach":    {ID: "reach", HostName: "alpha", OS: "macOS", UserID: 7, Online: true, TailscaleIPs: []string{"100.100.1.2"}, CurAddr: "203.0.113.5:41641", Relay: "fra", LastSeen: seen},
			"relayed":  {ID: "relayed", HostName: "bravo", OS: "linux", UserID: 7, Online: true, TailscaleIPs: []string{"100.100.1.3"}, Relay: "sin"},
			"silent":   {ID: "silent", HostName: "charlie", UserID: 7, Online: true, TailscaleIPs: []string{"100.100.1.4"}},
			"stranger": {ID: "stranger", HostName: "delta", UserID: 99, Online: true, TailscaleIPs: []string{"100.100.1.5"}},
			"asleep":   {ID: "asleep", HostName: "echo", UserID: 7, Online: false, TailscaleIPs: []string{"100.100.1.6"}},
		},
	}
}

// fixtureProbe answers for reach and relayed, fails for silent, and fails the test if any other peer is probed.
func fixtureProbe(t *testing.T) probeFunc {
	return func(_ context.Context, peer *tailnetPeer) (peerProbe, bool) {
		switch peer.ID {
		case "reach", "relayed":
			return peerProbe{Node: TailnetNode{ID: peer.ID, Name: peer.HostName, Online: true}, RTT: 12 * time.Millisecond}, true
		case "silent":
			return peerProbe{}, false
		}
		t.Errorf("peer %q should not be probed", peer.ID)
		return peerProbe{}, false
	}
}

func TestTopologyClassifiesEveryState(t *testing.T) {
	result, err := topologyFrom(context.Background(), topologyFixture(), SharingConfig{Enabled: true}, fixtureProbe(t))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]topologyPeer{}
	for _, peer := range result.Peers {
		byID[peer.ID] = peer
	}
	want := map[string]struct{ state, connection string }{
		"reach":    {"reachable", "direct"},
		"relayed":  {"reachable", "relay"},
		"silent":   {"no-gateway", ""},
		"stranger": {"not-allowed", ""},
		"asleep":   {"offline", ""},
	}
	if len(byID) != len(want) {
		t.Fatalf("got %d peers, want %d", len(byID), len(want))
	}
	for id, expected := range want {
		peer := byID[id]
		if peer.State != expected.state || peer.Connection != expected.connection {
			t.Errorf("%s: state=%q connection=%q, want state=%q connection=%q", id, peer.State, peer.Connection, expected.state, expected.connection)
		}
	}
	if byID["reach"].RTTms != 12 || byID["reach"].LastSeen.IsZero() {
		t.Errorf("reachable peer rtt/last_seen = %d / %v", byID["reach"].RTTms, byID["reach"].LastSeen)
	}
}

func TestTopologyJSONHasNoAddresses(t *testing.T) {
	result, err := topologyFrom(context.Background(), topologyFixture(), SharingConfig{Enabled: true}, fixtureProbe(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"100.100.1", "203.0.113.5", "CurAddr", "TailscaleIPs", "100.64"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("topology JSON leaks %q: %s", forbidden, data)
		}
	}
}

func TestTopologyDisabledSharingProbesNothing(t *testing.T) {
	result, err := topologyFrom(context.Background(), topologyFixture(), SharingConfig{Enabled: false}, func(context.Context, *tailnetPeer) (peerProbe, bool) {
		t.Error("disabled sharing must not probe")
		return peerProbe{}, false
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range result.Peers {
		if peer.State != "not-allowed" && peer.State != "offline" {
			t.Errorf("%s: state=%q with sharing disabled", peer.ID, peer.State)
		}
	}
}

func TestTailnetNodesFromReturnsOnlyAnsweringPeers(t *testing.T) {
	nodes, err := tailnetNodesFrom(context.Background(), topologyFixture(), SharingConfig{Enabled: true}, fixtureProbe(t))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	if strings.Join(ids, ",") != "reach,relayed" && strings.Join(ids, ",") != "relayed,reach" {
		t.Errorf("nodes = %v, want reach and relayed", ids)
	}
}
