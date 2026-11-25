package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/gateway/mesh"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
)

func TestValidTrackedPath(t *testing.T) {
	for _, good := range []string{"README.md", "src/app.ts", ".github/workflows/ci.yml", "docs/my file.md"} {
		if !validTrackedPath(good) {
			t.Errorf("validTrackedPath(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"", ".", "..", "../secret", "a/../b", "/etc/passwd", "a/b/", "a//b", `a\b`, "a\nb", "a\tb"} {
		if validTrackedPath(bad) {
			t.Errorf("validTrackedPath(%q) = true, want false", bad)
		}
	}
}

func TestProjectFilesAuthScopeAndTrackedOnly(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	repo := gitRepo(t)
	writeFile := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("src/app.ts", "export const app = 1\n")
	writeFile("sub/nested.go", "package sub\n")
	writeFile(".gitignore", "ignored.txt\n")
	git(t, repo, "add", "src/app.ts", "sub/nested.go", ".gitignore")
	git(t, repo, "commit", "-q", "-m", "add files")
	// Untracked and ignored files must never appear.
	writeFile("untracked.txt", "secret-untracked\n")
	writeFile("ignored.txt", "secret-ignored\n")
	writeFile("sub/untracked-sub.go", "package sub\n")
	// Remove a tracked file from working tree to prove index-only read (no content/working-tree read).
	if err := os.Remove(filepath.Join(repo, "src", "app.ts")); err != nil {
		t.Fatal(err)
	}

	nonGit := t.TempDir()
	writeFileInNonGit := filepath.Join(nonGit, "plain.txt")
	if err := os.WriteFile(writeFileInNonGit, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	g, origin := identityGateway(t, t.TempDir(), nil)
	testBrowserSession(t, g.auth, "viewer@example.com", "files-session")
	handler := g.Handler()

	call := func(dir string, authorized bool, actor *session.Actor) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, origin.String()+"/api/projects/files?directory="+url.QueryEscape(dir), nil)
		if authorized {
			req.AddCookie(&http.Cookie{Name: "agentpack_session", Value: "files-session"})
		}
		if actor != nil {
			req = req.WithContext(session.WithActor(req.Context(), *actor))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Unauthenticated request is rejected.
	if w := call(repo, false, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed: got %d, want 401", w.Code)
	}

	// Invalid, traversal, relative, non-existent, and file paths are rejected without leaking host paths.
	for _, bad := range []string{
		"",
		"relative/path",
		repo + "/sub/../src",
		filepath.Join(repo, "missing-dir"),
		writeFileInNonGit,
		repo + "\x00bad",
	} {
		w := call(bad, true, nil)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), repo) || strings.Contains(w.Body.String(), nonGit) {
			t.Errorf("GET directory=%q: got %d %q, want 400 without leaking path", bad, w.Code, w.Body.String())
		}
	}

	// Non-browser actor (MCP) cannot read an unlisted folder, only a known recent folder.
	mcpActor := &session.Actor{Kind: "mcp", User: "viewer@example.com"}
	if w := call(repo, true, mcpActor); w.Code != http.StatusBadRequest {
		t.Fatalf("mcp unlisted folder: got %d %s, want 400", w.Code, w.Body.String())
	}
	if err := remember(g.options.Home, session.SessionInfo{Directory: repo, Harness: "codex", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	if w := call(repo, true, mcpActor); w.Code != http.StatusOK {
		t.Fatalf("mcp known folder: got %d %s, want 200", w.Code, w.Body.String())
	}

	// Browser actor reads tracked files only, relative to the selected folder.
	w := call(repo, true, nil)
	var got projectFiles
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("repo files: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	wantRepo := []string{".gitignore", "README.md", "src/app.ts", "sub/nested.go"}
	if got.Truncated || !slices.Equal(got.Files, wantRepo) {
		t.Fatalf("repo files = %+v, want %v (truncated=false)", got, wantRepo)
	}

	// Selecting a subdirectory inside a repo returns only paths relative to that subdirectory.
	w = call(filepath.Join(repo, "sub"), true, nil)
	got = projectFiles{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("subdir files: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if got.Truncated || !slices.Equal(got.Files, []string{"nested.go"}) {
		t.Fatalf("subdir files = %+v, want [nested.go]", got)
	}

	// Non-git startable directory succeeds with an empty list.
	w = call(nonGit, true, nil)
	got = projectFiles{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("non-git files: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if got.Truncated || len(got.Files) != 0 {
		t.Fatalf("non-git files = %+v, want empty", got)
	}
}

func TestProjectFilesBounds(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	repo := gitRepo(t)
	for i := range 6 {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(repo, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "add six files")

	got, err := listTrackedProjectFiles(t.Context(), repo, 512<<10, 3)
	if err != nil || !got.Truncated || len(got.Files) != 3 {
		t.Fatalf("count bound: got %+v, err=%v; want 3 truncated files", got, err)
	}
	got, err = listTrackedProjectFiles(t.Context(), repo, 25, 2000)
	if err != nil || !got.Truncated || !slices.Equal(got.Files, []string{".gitignore", "README.md"}) {
		t.Fatalf("byte bound: got %+v, err=%v; want [.gitignore README.md] truncated", got, err)
	}
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := listTrackedProjectFiles(canceledCtx, repo, 512<<10, 2000); err == nil {
		t.Fatal("canceled context succeeded")
	}
}

func TestProjectFilesPeerRouting(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	repo := gitRepo(t)
	home := t.TempDir()
	g, _ := identityGateway(t, home, nil)
	store, err := registry.New(home)
	if err != nil {
		t.Fatal(err)
	}
	g.options.Library.Store = store
	const peerID = "peer-node-1"
	if err := store.EnrollPeer(peerID); err != nil {
		t.Fatal(err)
	}
	testBrowserSession(t, g.auth, testBrowserEmail, "peer-files")

	var identity mesh.Identity
	identity.Node.StableID = peerID
	identity.Node.ComputedName = "birch"
	identity.UserProfile.LoginName = "viewer@tailnet"
	self := &mesh.Peer{ID: "self", UserID: 7}

	callPeer := func(dir, user string, browser bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://100.64.0.2:7421/api/projects/files?directory="+url.QueryEscape(dir), nil)
		if user != "" {
			r.Header.Set("X-Agentpack-User", user)
		}
		if browser {
			r.Header.Set("X-Agentpack-Browser", "1")
		}
		w := httptest.NewRecorder()
		g.ServePeer(w, r, self, identity)
		return w
	}

	// Enrolled peer without forwarded browser cannot read an unlisted folder.
	if w := callPeer(repo, testBrowserEmail, false); w.Code != http.StatusBadRequest {
		t.Fatalf("peer without browser header on unlisted folder: got %d %s, want 400", w.Code, w.Body.String())
	}
	// Enrolled peer with forwarded browser can read a startable folder on the peer.
	w := callPeer(repo, testBrowserEmail, true)
	var got projectFiles
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("peer with forwarded browser: got %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if !slices.Equal(got.Files, []string{".gitignore", "README.md"}) {
		t.Fatalf("peer files = %+v, want [.gitignore README.md]", got)
	}
	// Unenrolled peer is refused.
	if err := store.RemovePeer(peerID); err != nil {
		t.Fatal(err)
	}
	if w := callPeer(repo, testBrowserEmail, true); w.Code != http.StatusForbidden {
		t.Fatalf("removed peer: got %d %s, want 403", w.Code, w.Body.String())
	}
}
