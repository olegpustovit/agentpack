package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/olegpustovit/agentpack/internal/session"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func folderFixture(t *testing.T, root string, paths ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{root}, paths...)...)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func folderTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	return home
}

func TestFolderListing(t *testing.T) {
	home := folderTestHome(t)
	g, origin := identityGateway(t, t.TempDir(), nil)
	testBrowserSession(t, g.auth, "viewer@example.com", "browse-folders")
	g.options.Packages = func(context.Context, string) (ProjectContents, error) {
		return ProjectContents{Packages: []ProjectPackage{{Module: "example/pack"}}}, nil
	}
	project := folderFixture(t, home, "project")
	folderFixture(t, home, ".hidden")
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "agentpack.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if gitAvailable() {
		if out, err := exec.Command("git", "init", "-q", "-b", "main", project).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	linked := os.Symlink(project, filepath.Join(home, "link")) == nil
	handler := g.Handler()
	get := func(path string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", origin.String()+"/api/folders?path="+url.QueryEscape(path), nil)
		if authorized {
			req.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "browse-folders"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		path       string
		authorized bool
		status     int
	}{
		{"~/", false, http.StatusUnauthorized},
		{"project", true, http.StatusBadRequest},
		{filepath.Join(home, "missing-private"), true, http.StatusNotFound},
		{filepath.Join(home, "notes.txt"), true, http.StatusNotFound},
	} {
		w := get(tc.path, tc.authorized)
		if w.Code != tc.status || strings.Contains(w.Body.String(), home) {
			t.Errorf("GET %q: got %d %s, want %d without a path", tc.path, w.Code, w.Body.String(), tc.status)
		}
	}
	w := get("~/", true)
	var listing folderListing
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || w.Code != 200 {
		t.Fatalf("listing: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if listing.Path != home || listing.Parent != filepath.Dir(home) || listing.Row == nil || listing.Row.Directory != home || len(listing.Row.Packages) != 1 || len(listing.Row.Launches) != 0 {
		t.Fatalf("listing metadata: %+v", listing)
	}
	if n := len(listing.Entries); n != 2 && !(linked && n == 3) || !listing.Entries[0].Hidden || listing.Entries[0].Name != ".hidden" {
		t.Fatalf("directory entries: %+v", listing.Entries)
	}
	entry := listing.Entries[len(listing.Entries)-1]
	if entry.Name != "project" || !entry.Pack || entry.Hidden || gitAvailable() && entry.Git != "main" {
		t.Fatalf("project entry: %+v", entry)
	}
	for i := range 1000 {
		folderFixture(t, home, fmt.Sprintf("folder-%04d", i))
	}
	listing, err := g.listFolders(t.Context(), home)
	if err != nil || !listing.Truncated || len(listing.Entries) != 1000 {
		t.Fatalf("bounded listing: %v, %d entries, truncated=%t", err, len(listing.Entries), listing.Truncated)
	}
}

func TestFoundFoldersUseBoundedDiscoveryAndLiveRecents(t *testing.T) {
	home := folderTestHome(t)
	g, _ := identityGateway(t, folderFixture(t, home, "data"), nil)
	mark := func(marker string, path ...string) string {
		dir := folderFixture(t, home, path...)
		if err := os.WriteFile(filepath.Join(dir, marker), nil, 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	repo := mark(".git", "projects", "repo")
	mark("agentpack.toml", "projects", "repo", "nested")
	pack := mark("agentpack.toml", "src", "a", "b", "pack")
	mark("agentpack.toml", "src", "a", "b", "pack", "too-deep")
	for _, skipped := range []string{"node_modules", "vendor", ".hidden", "Library", "AppData"} {
		mark("agentpack.toml", "code", skipped, "skip")
	}
	mark("agentpack.toml", "data", "worktrees", "repo", "skip")
	outside := folderFixture(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(outside, "agentpack.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(home, "linked"))
	rows, err := g.foundFolders(t.Context())
	directories := func(rows []ProjectRow) []string {
		var paths []string
		for _, row := range rows {
			paths = append(paths, row.Directory)
		}
		slices.Sort(paths)
		return paths
	}
	if want := []string{repo, pack}; err != nil || !slices.Equal(directories(rows), want) {
		t.Fatalf("found %v (%v), want %v", directories(rows), err, want)
	}
	if err := remember(g.options.Home, session.SessionInfo{Directory: repo, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	mark("agentpack.toml", "projects", "created-after-scan")
	rows, err = g.foundFolders(t.Context())
	if err != nil || !slices.Equal(directories(rows), []string{pack}) {
		t.Fatalf("cached discovery must still exclude new recents: %v (%v)", directories(rows), err)
	}
}

func TestBrowserStartsInNewFolder(t *testing.T) {
	home := t.TempDir()
	var g *Gateway
	g, origin := identityGateway(t, home, func(ctx context.Context, info session.SessionInfo, _ string) (string, error) {
		if info.Transport == "acp" {
			return info.ID, nil
		}
		// The terminal worker registers its session before answering the gateway.
		info.ID = "session-terminal"
		info.LastUsed = time.Now().UTC()
		return info.ID, session.UpsertSession(ctx, g.store.DB, info)
	})
	t.Cleanup(g.starting.Wait)
	testBrowserSession(t, g.auth, "viewer@example.com", "browse-start")
	handler := g.Handler()
	call := func(method, path string, input chatStartRequest, actor *session.Actor) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(string(body)))
		r.Header.Set("Origin", origin.String())
		r.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "browse-start"})
		if actor != nil {
			r = r.WithContext(session.WithActor(r.Context(), *actor))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	unlisted := t.TempDir()
	file := filepath.Join(unlisted, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	chat, terminal := t.TempDir(), t.TempDir()
	mcp := &session.Actor{Kind: "mcp", User: "viewer@example.com"}
	for i, tc := range []struct {
		dir, transport string
		actor          *session.Actor
		status         int
	}{
		{unlisted, "acp", mcp, 400},
		{unlisted, "terminal", mcp, 400},
		{file, "acp", nil, 400},
		{filepath.Join(unlisted, "missing"), "terminal", nil, 400},
		{"relative", "terminal", nil, 400},
		{chat, "acp", nil, 200},
		{terminal, "terminal", nil, 200},
	} {
		input := chatStartRequest{Transport: tc.transport, Directory: tc.dir, Harness: "claude", Mode: "default"}
		if tc.transport == "acp" {
			input.ID = fmt.Sprintf("session-%032x", i+1)
		}
		w := call("POST", "/api/sessions", input, tc.actor)
		if w.Code != tc.status {
			t.Errorf("%s start in %q: got %d %s, want %d", tc.transport, tc.dir, w.Code, w.Body.String(), tc.status)
		}
	}
	w := call("GET", "/api/projects", chatStartRequest{}, nil)
	var projects []ProjectRow
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Fatalf("projects: %d %s: %v", w.Code, w.Body.String(), err)
	}
	for _, dir := range []string{chat, terminal} {
		if !slices.ContainsFunc(projects, func(row ProjectRow) bool { return row.Available && row.Directory == dir && len(row.Launches) == 1 }) {
			t.Errorf("new launch in %q missing from projects: %+v", dir, projects)
		}
	}
}
