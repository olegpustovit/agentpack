package network

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

func TestRecentForgetsOnlyMatchingLaunchWithoutDeletingFolder(t *testing.T) {
	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	first := SessionInfo{Directory: directory, Harness: "codex", Mode: "default"}
	second := first
	second.Mode = "review"
	for _, entry := range []SessionInfo{first, second, first} {
		if err := remember(home, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Recent(home)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	if err := ForgetRecent(t.Context(), testSessionStore(t, home), first); err != nil {
		t.Fatal(err)
	}
	entries, err = Recent(home)
	if err != nil || len(entries) != 1 || entries[0].Mode != "review" {
		t.Fatal(entries, err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal("forget removed user folder", err)
	}
}

func TestGatewayDisabledPersistsUntilExplicitStart(t *testing.T) {
	home := t.TempDir()
	if enabled, err := GatewayEnabled(home); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	if err := SetGatewayEnabled(home, false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := GatewayEnabled(home); err != nil || enabled {
		t.Fatal(enabled, err)
	}
	if err := SetGatewayEnabled(home, true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := GatewayEnabled(home); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
}

func TestKnownDirectoryRejectsReplacementUntilLocalLaunch(t *testing.T) {
	home := t.TempDir()
	if err := paths.EnsurePrivateDirectory(filepath.Join(home, "network")); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	info := SessionInfo{Directory: directory, Harness: "codex"}
	if err := remember(home, info); err != nil {
		t.Fatal(err)
	}
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	gateway := Gateway{store: auth.store, auth: auth, options: GatewayOptions{Home: home}}
	if !gateway.knownDirectory(t.Context(), directory) {
		t.Fatal("remembered folder rejected")
	}
	// Keep the original alive so filesystem identity cannot be recycled.
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if gateway.knownDirectory(t.Context(), directory) {
		t.Fatal("replacement folder trusted without a local launch")
	}
	if err := remember(home, info); err != nil {
		t.Fatal(err)
	}
	if !gateway.knownDirectory(t.Context(), directory) {
		t.Fatal("local launch did not authorize replacement")
	}
}

func TestRecentAPIReportsCurrentAvailabilityAndLastUse(t *testing.T) {
	home := t.TempDir()
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	info := SessionInfo{Directory: directory, Harness: "codex", Mode: "default"}
	if err := remember(home, info); err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://agentpack.example")
	g := &Gateway{store: auth.store, auth: auth, origin: origin, local: nodeRecord{Secret: strings.Repeat("x", 64)}, options: GatewayOptions{Home: home}}
	handler := g.Handler()
	read := func() RecentFolder {
		t.Helper()
		request := httptest.NewRequest("GET", origin.String()+"/api/recent", nil)
		request.Header.Set("Authorization", "Bearer "+g.local.Secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var folders []RecentFolder
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &folders); err != nil {
			t.Fatal(err)
		}
		if len(folders) != 1 {
			t.Fatal(folders)
		}
		return folders[0]
	}
	first := read()
	if !first.Available || first.LastUsed.Before(before) || first.LastUsed.After(time.Now()) {
		t.Fatal(first)
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	missing := read()
	if missing.Available || missing.UnavailableReason != "Folder is missing or inaccessible" || !missing.LastUsed.Equal(first.LastUsed) {
		t.Fatal(missing)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	replaced := read()
	if replaced.Available || replaced.UnavailableReason != "Folder changed since its last local launch" {
		t.Fatal(replaced)
	}
	persisted, err := Recent(home)
	if err != nil || len(persisted) != 1 || persisted[0].FolderID != first.FolderID {
		t.Fatalf("availability check changed folder authorization: %+v %v", persisted, err)
	}
	if _, err := os.Stat(filepath.Join(home, "network", "recent.json")); !os.IsNotExist(err) {
		t.Fatalf("recent JSON was written: %v", err)
	}
	if err := remember(home, info); err != nil {
		t.Fatal(err)
	}
	refreshed := read()
	if !refreshed.Available || !refreshed.LastUsed.After(first.LastUsed) {
		t.Fatal(refreshed)
	}
}

func TestKnownDirectoryBeyondRecentList(t *testing.T) {
	home, directory := t.TempDir(), t.TempDir()
	if err := remember(home, SessionInfo{Directory: directory, Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	auth, err := testGatewayAuth(t, home)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.store.DB.Exec(`WITH RECURSIVE newer(i) AS (
		SELECT 1 UNION ALL SELECT i+1 FROM newer WHERE i<100
	) INSERT INTO sessions(id,node_id,directory,last_used)
	SELECT 'known-directory-test-'||i,`+state.LocalNodeSQL+`,?||i,? FROM newer`, directory, time.Now().Add(time.Hour).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{store: auth.store, auth: auth}
	if !g.knownDirectory(t.Context(), directory) {
		t.Fatal("a known folder outside the recent list was rejected")
	}
}
