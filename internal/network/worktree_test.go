package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gitRepo makes a repository with one commit and isolates git from the user's configuration.
func gitRepo(t *testing.T) string {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		t.Setenv(key, value)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "README.md"), "hello\n")
	write(t, filepath.Join(dir, ".gitignore"), ".env\nsecrets/\nnode_modules/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(context.Background(), gitTimeout, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWorktreeCarriesChangesAndCopiesIncludes(t *testing.T) {
	main := gitRepo(t)
	home := t.TempDir()
	write(t, filepath.Join(main, ".worktreeinclude"), "secrets\n")
	write(t, filepath.Join(main, ".env"), "TOKEN=1\n")
	write(t, filepath.Join(main, "secrets", "key"), "k\n")
	write(t, filepath.Join(main, "node_modules", "dep.js"), "x\n")
	write(t, filepath.Join(main, "README.md"), "changed\n")
	if err := os.Symlink("/etc/hostname", filepath.Join(main, "secrets", "escape")); err != nil {
		t.Fatal(err)
	}

	created, err := createWorktree(t.Context(), home, main, worktreeSlug("Fix the overlay cleanup!"), true)
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "ap/fix-the-overlay-cleanup" || !insideWorktrees(home, created.Path) || created.Directory != created.Path {
		t.Fatalf("created %+v", created)
	}
	for file, want := range map[string]string{".env": "TOKEN=1\n", "secrets/key": "k\n", "README.md": "changed\n"} {
		if got, err := os.ReadFile(filepath.Join(created.Path, file)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", file, got, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(created.Path, "node_modules")); !os.IsNotExist(err) {
		t.Errorf("node_modules was copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(created.Path, "secrets", "escape")); !os.IsNotExist(err) {
		t.Errorf("a link out of the tree was copied: %v", err)
	}
	if list := git(t, main, "stash", "list"); list != "" {
		t.Errorf("carry left a stash entry: %q", list)
	}
	if status := git(t, main, "status", "--porcelain", "--untracked-files=no"); status != "M README.md" {
		t.Errorf("main folder changed: %q", status)
	}
	again, err := createWorktree(t.Context(), home, main, "fix-the-overlay-cleanup", false)
	if err != nil || again.Branch != "ap/fix-the-overlay-cleanup-2" {
		t.Fatalf("second worktree %+v, %v", again, err)
	}
	if got, _ := os.ReadFile(filepath.Join(again.Path, "README.md")); string(got) != "hello\n" {
		t.Errorf("uncarried worktree has %q", got)
	}
}

func TestWorktreeTrustCheckRejectsOtherFolders(t *testing.T) {
	main := gitRepo(t)
	home := t.TempDir()
	created, err := createWorktree(t.Context(), home, main, "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, root, err := worktreeMain(t.Context(), home, created.Path); err != nil || got != main || root != created.Path {
		t.Fatalf("worktreeMain = %q %q %v", got, root, err)
	}
	if isWorktreeOf(t.Context(), main, main) || isWorktreeOf(t.Context(), main, t.TempDir()) {
		t.Fatal("isWorktreeOf accepted a folder git does not list")
	}
	// A separate repository placed under the worktrees folder is still not a worktree of main.
	impostor := filepath.Join(worktreesRoot(home), "0000", "impostor")
	if err := os.MkdirAll(impostor, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, impostor, "init", "-q")
	for _, dir := range []string{impostor, main, t.TempDir()} {
		if _, _, err := worktreeMain(t.Context(), home, dir); err == nil {
			t.Errorf("worktreeMain accepted %s", dir)
		}
	}
}

func TestRemoveWorktreeRefusesChangesUnlessDiscarded(t *testing.T) {
	main := gitRepo(t)
	home := t.TempDir()
	created, err := createWorktree(t.Context(), home, main, "dirty", false)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(created.Path, "README.md"), "edited\n")
	if err := removeWorktree(t.Context(), home, created.Path, false); err == nil {
		t.Fatal("removed a worktree with changes")
	}
	if err := removeWorktree(t.Context(), home, created.Path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if err := removeWorktree(t.Context(), home, main, true); err == nil {
		t.Fatal("removed a folder outside the worktrees")
	}
}

// worktreeGateway knows main as a recent folder and starts chats without a worker.
func worktreeGateway(t *testing.T) (*Gateway, string, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	main := gitRepo(t)
	home := t.TempDir()
	g, origin := identityGateway(t, home, func(_ context.Context, info SessionInfo, _ string) (string, error) { return info.ID, nil })
	g.options.Packages = func(context.Context, string) (ProjectContents, error) { return ProjectContents{}, nil }
	if err := remember(home, SessionInfo{Directory: main, Harness: "claude", Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	handler := g.Handler()
	return g, main, func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin.String()+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
}

func startWorktreeChat(t *testing.T, g *Gateway, call func(method, path, body string) *httptest.ResponseRecorder, main, worktree string) SessionInfo {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"directory": main, "harness": "claude", "mode": "default", "transport": "acp", "worktree": worktree, "worktree_name": "replay offset"})
	w := call("POST", "/api/sessions", string(body))
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	var info SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	g.starting.Wait()
	if _, err := g.auth.store.DB.Exec("UPDATE sessions SET ended_at=1, conversation_state='idle' WHERE id=?", info.ID); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestNewWorktreeSessionSharesTheProjectAndArchiveRemovesIt(t *testing.T) {
	g, main, call := worktreeGateway(t)
	info := startWorktreeChat(t, g, call, main, "new")
	if !insideWorktrees(g.options.Home, info.Directory) || info.DefinitionRoot != main {
		t.Fatalf("session %+v", info)
	}
	if stored, err := storedSession(t.Context(), g.auth.store.Reader, info.ID); err != nil || stored.WorktreeOf != main {
		t.Fatalf("worktree_of %q %v", stored.WorktreeOf, err)
	}
	// A second chat continues in the same worktree; an arbitrary folder is refused.
	other := startWorktreeChat(t, g, call, main, info.Directory)
	if other.Directory != info.Directory {
		t.Fatalf("continue in worktree ran in %s", other.Directory)
	}
	body, _ := json.Marshal(map[string]any{"directory": main, "harness": "claude", "transport": "acp", "worktree": t.TempDir()})
	if w := call("POST", "/api/sessions", string(body)); w.Code != 422 {
		t.Fatalf("arbitrary worktree: %d %s", w.Code, w.Body)
	}
	projects, err := g.projects(t.Context())
	if err != nil || len(projects) != 1 || projects[0].Directory != main || projects[0].Git == nil || projects[0].Git.Branch != "main" {
		t.Fatalf("projects %+v %v", projects, err)
	}

	// Archiving one chat keeps the worktree its sibling still uses; the last one removes it.
	if w := call("PATCH", "/api/sessions/"+info.ID, `{"hidden":true}`); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(info.Directory); err != nil {
		t.Fatalf("worktree removed while in use: %v", err)
	}
	if w := call("PATCH", "/api/sessions/"+other.ID, `{"hidden":true}`); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(info.Directory); !os.IsNotExist(err) {
		t.Fatalf("clean merged worktree kept: %v", err)
	}
}

func TestSessionGitCommitAndPullRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	g, main, call := worktreeGateway(t)
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare")
	git(t, main, "remote", "add", "origin", origin)
	bin := t.TempDir()
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\ncase \"$1 $2\" in\n\"pr view\") exit 1 ;;\n\"pr create\") echo https://github.com/o/r/pull/7 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath, _ := exec.LookPath("git")
	gitOnly := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(gitOnly, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+gitOnly)

	info := startWorktreeChat(t, g, call, main, "new")
	write(t, filepath.Join(info.Directory, "feature.go"), "package x\n")
	var state sessionGit
	w := call("GET", "/api/sessions/"+info.ID+"/git", "")
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.Worktree || state.Branch != "ap/replay-offset" || state.Base != "main" || state.Untracked != 1 || !state.GH {
		t.Fatalf("git state %s %v", w.Body, err)
	}
	if w := call("POST", "/api/sessions/"+info.ID+"/git/commit", `{"message":"Add feature"}`); w.Code != 200 {
		t.Fatalf("commit: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/sessions/"+info.ID+"/git/commit", `{"message":"Again"}`); w.Code != 409 {
		t.Fatalf("empty commit: %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/sessions/"+info.ID+"/git", "")
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || state.Ahead != 1 || state.Merged || state.Added != 1 {
		t.Fatalf("after commit %s", w.Body)
	}
	w = call("POST", "/api/sessions/"+info.ID+"/git/pr", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://github.com/o/r/pull/7") {
		t.Fatalf("pr: %d %s", w.Code, w.Body)
	}
	if git(t, origin, "rev-parse", "ap/replay-offset") == "" {
		t.Fatal("branch not pushed")
	}

	// Without gh the answer is GitHub's compare page.
	t.Setenv("PATH", gitOnly)
	git(t, main, "remote", "set-url", "origin", "git@github.com:o/r.git")
	git(t, main, "config", "remote.origin.pushurl", origin)
	w = call("POST", "/api/sessions/"+info.ID+"/git/pr", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://github.com/o/r/compare/main...ap%2Freplay-offset?expand=1") {
		t.Fatalf("compare: %d %s", w.Code, w.Body)
	}
}

// A squash-merged pull request leaves no merge in the main folder; gh's MERGED state for the
// commit the worktree has checked out is what lets archiving remove it.
func TestArchiveRemovesWorktreeOfMergedPullRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	ttl := prCacheTTL
	prCacheTTL = 0
	t.Cleanup(func() { prCacheTTL = ttl })
	g, main, call := worktreeGateway(t)
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare")
	git(t, main, "remote", "add", "origin", origin)
	info := startWorktreeChat(t, g, call, main, "new")
	write(t, filepath.Join(info.Directory, "feature.go"), "package x\n")
	git(t, info.Directory, "add", "-A")
	git(t, info.Directory, "commit", "-q", "-m", "feature")
	git(t, info.Directory, "push", "-q", "-u", "origin", "ap/replay-offset")
	head := git(t, info.Directory, "rev-parse", "HEAD")
	bin := t.TempDir()
	gh := func(state string) {
		write(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho '{\"number\":7,\"url\":\"https://github.com/o/r/pull/7\",\"state\":\""+state+"\",\"headRefOid\":\""+head+"\",\"statusCheckRollup\":[]}'\n")
		if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitPath, _ := exec.LookPath("git")
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	rowMerged := func() bool {
		var body struct{ Worktrees []worktreeRow }
		if err := json.Unmarshal(call("GET", "/api/worktrees", "").Body.Bytes(), &body); err != nil || len(body.Worktrees) != 1 {
			t.Fatalf("worktrees %+v %v", body, err)
		}
		return body.Worktrees[0].Merged
	}

	gh("OPEN")
	if rowMerged() {
		t.Fatal("open pull request reads as merged")
	}
	gh("MERGED")
	if !rowMerged() {
		t.Fatal("merged pull request reads as unmerged")
	}
	if w := call("PATCH", "/api/sessions/"+info.ID, `{"hidden":true}`); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(info.Directory); !os.IsNotExist(err) {
		t.Fatalf("worktree of a merged pull request kept: %v", err)
	}
}

func TestSessionMergeConflictAbortsAndAnswers409(t *testing.T) {
	g, main, call := worktreeGateway(t)
	info := startWorktreeChat(t, g, call, main, "new")
	write(t, filepath.Join(info.Directory, "README.md"), "worktree\n")
	git(t, info.Directory, "commit", "-q", "-am", "worktree edit")
	write(t, filepath.Join(main, "README.md"), "main\n")
	git(t, main, "commit", "-q", "-am", "main edit")
	w := call("POST", "/api/sessions/"+info.ID+"/git/merge", `{}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"conflict":true`) {
		t.Fatalf("merge: %d %s", w.Code, w.Body)
	}
	if status := git(t, main, "status", "--porcelain"); status != "" {
		t.Fatalf("merge left the main folder %q", status)
	}
	// A shared folder session cannot commit: other agents' edits would be swept in.
	plain, err := g.startSession(t.Context(), LocalActor("local"), SessionInfo{Directory: main, Harness: "claude", Mode: "default", Transport: "acp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	g.starting.Wait()
	if w := call("POST", "/api/sessions/"+plain.ID+"/git/commit", `{"message":"x"}`); w.Code != 409 {
		t.Fatalf("shared folder commit: %d %s", w.Code, w.Body)
	}
}

func TestCompareURL(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/o/r.git":    "https://github.com/o/r/compare/main...ap%2Fx?expand=1",
		"git@github.com:o/r":            "https://github.com/o/r/compare/main...ap%2Fx?expand=1",
		"https://gitlab.com/o/r.git":    "",
		"https://token@github.com/o/r/": "https://github.com/o/r/compare/main...ap%2Fx?expand=1",
		"ssh://git@github.com/o/r.git":  "https://github.com/o/r/compare/main...ap%2Fx?expand=1",
	} {
		if got := compareURL(remote, "main", "ap/x"); got != want {
			t.Errorf("compareURL(%q) = %q", remote, got)
		}
	}
}

func TestClientChatClaimPreparesWorktreeOnce(t *testing.T) {
	g, main, call := worktreeGateway(t)
	input := chatStartRequest{
		ID: "session-cc548a740d2c495991c792576427836d", Directory: main,
		Harness: "claude", Mode: "default", Transport: "acp", Worktree: "new", WorktreeName: "claimed",
	}
	body := string(mustRawJSON(input))
	start := call(http.MethodPost, "/api/sessions", body)
	var provisional SessionInfo
	if err := json.Unmarshal(start.Body.Bytes(), &provisional); start.Code != http.StatusOK || err != nil ||
		provisional.ID != input.ID || provisional.Directory != main || provisional.ConversationState != "starting" {
		t.Fatalf("new worktree claim = %d %s", start.Code, start.Body.String())
	}
	g.starting.Wait()
	replay := call(http.MethodPost, "/api/sessions", body)
	var prepared SessionInfo
	if err := json.Unmarshal(replay.Body.Bytes(), &prepared); replay.Code != http.StatusOK || err != nil ||
		prepared.ID != provisional.ID || prepared.Directory == main || prepared.WorktreeOf != main {
		t.Fatalf("worktree claim retry = %d %s", replay.Code, replay.Body.String())
	}
	records, err := listWorktrees(t.Context(), main)
	if err != nil || len(records) != 2 {
		t.Fatalf("expected main and one worktree, got %+v (%v)", records, err)
	}
}

func TestSessionDiffstatRefreshesIndexAndUnstagedEdits(t *testing.T) {
	dir := gitRepo(t)
	path := filepath.Join(dir, "README.md")
	check := func(want int) {
		t.Helper()
		status, err := readStatus(t.Context(), dir)
		if err != nil {
			t.Fatal(err)
		}
		added, deleted := sessionDiffstat(t.Context(), dir, "HEAD", status)
		if added != want || deleted != 0 {
			t.Fatalf("lines changed = +%d -%d, want +%d", added, deleted, want)
		}
	}
	check(0)
	write(t, path, "hello\nsecond\n")
	git(t, dir, "add", "README.md")
	check(1)
	check(1)
	write(t, path, "hello\nsecond\nthird\n")
	check(2)
	git(t, dir, "add", "README.md")
	check(2)
	write(t, path, "hello\nsecond\nthird\nfourth\n")
	check(3)
}
