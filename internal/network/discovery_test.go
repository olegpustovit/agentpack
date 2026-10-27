package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
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
	if !peerRoute("GET", "/api/sessions/x/terminal") || !peerRoute("POST", "/api/sessions/x/restart") {
		t.Fatal("terminal routes unavailable")
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
