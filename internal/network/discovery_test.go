package network

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestPersonalTailnetPolicy(t *testing.T) {
	self := &tailnetPeer{ID: "self", UserID: 7}
	for _, test := range []struct {
		name  string
		peer  *tailnetPeer
		allow []string
		want  bool
	}{
		{"own device", &tailnetPeer{ID: "own", UserID: 7}, nil, true},
		{"other user", &tailnetPeer{ID: "other", UserID: 8}, nil, false},
		{"no user", &tailnetPeer{ID: "unknown"}, nil, false},
		{"tag requires permission", &tailnetPeer{ID: "server", UserID: 7, Tags: []string{"tag:server"}}, nil, false},
		{"explicit server", &tailnetPeer{ID: "server", UserID: 8, Tags: []string{"tag:server"}}, []string{"server"}, true},
		{"explicit tag", &tailnetPeer{ID: "server", Tags: []string{"tag:server"}}, []string{"tag:server"}, true},
		{"other tag", &tailnetPeer{ID: "server", Tags: []string{"tag:other"}}, []string{"tag:server"}, false},
		{"missing identity", nil, []string{"server"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowedTailnetPeer(self, test.peer, SharingConfig{Allow: test.allow}); got != test.want {
				t.Fatalf("allowed=%v want %v", got, test.want)
			}
		})
	}
	self.Tags = []string{"tag:gateway"}
	if allowedTailnetPeer(self, &tailnetPeer{ID: "own", UserID: 7}, SharingConfig{}) {
		t.Fatal("tagged gateway must explicitly allow personal clients")
	}
}
func TestWhoIsRequiresExactDeviceAddress(t *testing.T) {
	self := &tailnetPeer{ID: "self", UserID: 7}
	var identity tailnetIdentity
	identity.Node.StableID = "own"
	identity.Node.User = 7
	address := netip.MustParseAddr("100.100.1.2")
	for _, test := range []struct {
		prefix string
		want   bool
	}{{"100.100.1.2/32", true}, {"100.100.1.3/32", false}, {"100.100.1.0/24", false}, {"invalid", false}} {
		identity.Node.Addresses = []string{test.prefix}
		if got := allowedTailnetIdentity(self, identity, address, SharingConfig{}); got != test.want {
			t.Fatalf("%s allowed=%v", test.prefix, got)
		}
	}
	identity.Node.Addresses = []string{"100.100.1.2/32"}
	identity.Node.User = 8
	if allowedTailnetIdentity(self, identity, address, SharingConfig{}) {
		t.Fatal("different user admitted")
	}
}
func TestTailnetTargetsAndRoutesAreBounded(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "192.168.1.1", "example.com", "169.254.169.254", "::1"} {
		if tailnetAddress(&tailnetPeer{TailscaleIPs: []string{address}}) != "" {
			t.Fatalf("unsafe address %s", address)
		}
	}
	if got := tailnetAddress(&tailnetPeer{TailscaleIPs: []string{"100.100.1.2"}}); got != "100.100.1.2:7421" {
		t.Fatal(got)
	}
	if got := tailnetAddress(&tailnetPeer{TailscaleIPs: []string{"fd7a:115c:a1e0::1"}}); got != "[fd7a:115c:a1e0::1]:7421" {
		t.Fatal(got)
	}
	for _, path := range []string{"/api/setup", "/api/login", "/api/node/stop", "/oauth/token", "/mcp", "/api/sessions/../terminal", "/api/sessions/x/stop/extra"} {
		if peerRoute("POST", path) || peerRoute("GET", path) {
			t.Fatalf("unsafe route %s", path)
		}
	}
	if !peerRoute("GET", "/api/sessions/x/terminal") || !peerRoute("POST", "/api/sessions/x/restart") || !peerRoute("POST", "/api/node/update") {
		t.Fatal("terminal routes unavailable")
	}
	routes := []struct{ method, path string }{
		{"GET", "/api/agent-sessions"},
		{"GET", "/api/sessions/x/conversation"},
		{"GET", "/api/sessions/x/conversation/config-options"},
		{"POST", "/api/sessions/x/conversation/prompt"},
		{"POST", "/api/sessions/x/conversation/prompt/images"},
		{"POST", "/api/sessions/x/conversation/cancel"},
		{"POST", "/api/sessions/x/conversation/permission"},
		{"POST", "/api/sessions/x/conversation/elicitation"},
		{"POST", "/api/sessions/x/conversation/config"},
		{"POST", "/api/sessions/x/conversation/blobs"},
		{"POST", "/api/sessions/x/fork"},
		{"GET", "/api/sessions/x/blobs/" + strings.Repeat("a", 64)},
	}
	for _, route := range routes {
		if !peerRoute(route.method, route.path) {
			t.Errorf("route %s %s not available to peer", route.method, route.path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/sessions/../conversation/prompt/images"},
		{"POST", "/api/sessions/x/conversation/config-options"},
		{"GET", "/api/sessions/x/conversation/permission"},
		{"POST", "/api/sessions/x/blobs/" + strings.Repeat("a", 64)},
		{"GET", "/api/sessions/x/blobs/not-a-sha"},
		{"GET", "/api/sessions/x\\..\\conversation"},
	} {
		if peerRoute(route.method, route.path) {
			t.Errorf("unsafe peer route accepted: %s %s", route.method, route.path)
		}
	}
}

func TestSlowTailnetRequestsUseExtendedTransport(t *testing.T) {
	for _, request := range []struct {
		method string
		path   string
		want   bool
	}{
		{"GET", "/api/agent-sessions", true},
		{"GET", "/api/limits", true},
		{"GET", "/api/library/index", true},
		{"POST", "/api/sessions", true},
		{"GET", "/api/projects", false},
	} {
		if got := slowTailnetRequest(request.method, request.path); got != request.want {
			t.Errorf("slowTailnetRequest(%q, %q) = %t, want %t", request.method, request.path, got, request.want)
		}
	}
}

func TestAgentSessionRouteUsesBoundedProbeTransport(t *testing.T) {
	if got := tailnetRequestTransport("GET", "/api/agent-sessions"); got != tailnetProbeTransport {
		t.Fatal("agent session probe should use its 60-second response timeout")
	}
	if got := tailnetRequestTransport("GET", "/api/projects"); got != tailnetTransport {
		t.Fatal("ordinary read should retain the five-second response timeout")
	}
}
func TestSharingConfigPersistsAndValidates(t *testing.T) {
	home := t.TempDir()
	empty, err := ReadSharingConfig(home)
	if err != nil || !empty.Enabled {
		t.Fatalf("sharing should default on: %+v %v", empty, err)
	}
	if err := WriteSharingConfig(home, SharingConfig{}); err != nil {
		t.Fatal(err)
	}
	if off, err := ReadSharingConfig(home); err != nil || off.Enabled {
		t.Fatalf("explicit off must persist: %+v %v", off, err)
	}
	want := SharingConfig{Enabled: true, Allow: []string{"tag:server", "stable-node"}}
	if err := WriteSharingConfig(home, want); err != nil {
		t.Fatal(err)
	}
	actual, err := ReadSharingConfig(home)
	if err != nil || !actual.Enabled || len(actual.Allow) != 2 {
		t.Fatalf("loaded: %+v %v", actual, err)
	}
	if err := WriteSharingConfig(home, SharingConfig{Allow: []string{"http://arbitrary"}}); err == nil {
		t.Fatal("accepted arbitrary URL")
	}
	if err := os.WriteFile(filepath.Join(home, "network", "sharing.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSharingConfig(home); err == nil {
		t.Fatal("silently ignored invalid config")
	}
}

func TestLimitsPeerRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/limits", true},
		{"POST", "/api/limits/refresh", true},
		{"POST", "/api/limits", false},
		{"GET", "/api/limits/refresh", false},
		{"DELETE", "/api/limits", false},
		{"GET", "/api/limits/extra", false},
	} {
		if got := peerRoute(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s allowed=%v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

type closeRecorder chan struct{}

func (c closeRecorder) Close() error { close(c); return nil }

func TestCloseUpgradeOnEndClosesWhenGatewayStopsOrLoginEnds(t *testing.T) {
	wait := func(t *testing.T, closed closeRecorder) {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("connection was not closed")
		}
	}
	t.Run("gateway stops", func(t *testing.T) {
		closed, done := make(closeRecorder), make(chan struct{})
		go closeUpgradeOnEnd(context.Background(), done, closed, time.Hour, func() bool { return true })
		close(done)
		wait(t, closed)
	})
	t.Run("login ends", func(t *testing.T) {
		closed := make(closeRecorder)
		go closeUpgradeOnEnd(context.Background(), nil, closed, time.Millisecond, func() bool { return false })
		wait(t, closed)
	})
	t.Run("request ends", func(t *testing.T) {
		closed := make(closeRecorder)
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		go func() {
			closeUpgradeOnEnd(ctx, nil, closed, time.Hour, func() bool { return true })
			close(finished)
		}()
		cancel()
		<-finished
		select {
		case <-closed:
			t.Fatal("the proxy owns the connection once the request ends")
		default:
		}
	})
}

func TestTailnetStatusFromKeepsWhatPolicyReads(t *testing.T) {
	tags := views.SliceOf([]string{"tag:ci"})
	peer := &ipnstate.PeerStatus{ID: "n1", HostName: "mac", UserID: 7, Online: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.100.1.2")}, Tags: &tags}
	var nodeKey key.NodePublic
	status := tailnetStatusFrom(&ipnstate.Status{
		BackendState: "Running",
		Self:         &ipnstate.PeerStatus{ID: "self", UserID: 7},
		Peer:         map[key.NodePublic]*ipnstate.PeerStatus{nodeKey: peer},
		User:         map[tailcfg.UserID]tailcfg.UserProfile{7: {LoginName: "me@"}},
	})
	got := status.Peer[nodeKey.String()]
	if got == nil || got.ID != "n1" || got.UserID != 7 || !got.Online || !slices.Equal(got.Tags, []string{"tag:ci"}) {
		t.Fatalf("peer: %+v", got)
	}
	if tailnetAddress(got) != "100.100.1.2:7421" {
		t.Fatalf("address: %q", tailnetAddress(got))
	}
	if status.User["7"].LoginName != "me@" || status.Self.UserID != 7 {
		t.Fatalf("status: %+v", status)
	}
	if tailnetPeerFrom(nil) != nil {
		t.Fatal("a stopped daemon reports no Self")
	}
}

func TestOnlyTheSameUntaggedOwnerEnrollsAutomatically(t *testing.T) {
	self := &tailnetPeer{ID: "self", UserID: 7}
	cases := []struct {
		name string
		self *tailnetPeer
		peer *tailnetPeer
		want bool
	}{
		{"same owner", self, &tailnetPeer{ID: "p", UserID: 7}, true},
		{"another owner", self, &tailnetPeer{ID: "p", UserID: 8}, false},
		{"tagged peer", self, &tailnetPeer{ID: "p", UserID: 7, Tags: []string{"tag:ci"}}, false},
		{"tagged self", &tailnetPeer{ID: "self", UserID: 7, Tags: []string{"tag:ci"}}, &tailnetPeer{ID: "p", UserID: 7}, false},
		{"unknown owner", &tailnetPeer{ID: "self"}, &tailnetPeer{ID: "p"}, false},
	}
	for _, test := range cases {
		if got := ownMachine(test.self, test.peer); got != test.want {
			t.Errorf("%s: got %v", test.name, got)
		}
	}
}

func TestLibraryPeerRoutesCoverConflictsAndVersions(t *testing.T) {
	for _, test := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/library/agentpacks/abc/conflict", true},
		{"POST", "/api/library/agentpacks/abc/resolve", true},
		{"POST", "/api/library/latest", true},
		{"GET", "/api/library/latest", false},
		{"POST", "/api/library/agentpacks/abc/conflict", false},
		{"GET", "/api/library/agentpacks/abc/resolve", false},
	} {
		if got := libraryPeerRoute(test.method, test.path); got != test.want {
			t.Errorf("%s %s: got %v", test.method, test.path, got)
		}
	}
}
