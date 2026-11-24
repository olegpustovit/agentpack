package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
)

func TestMacOSFoldersDeferProtectedDetailsUntilOpened(t *testing.T) {
	home := folderTestHome(t)
	documents := folderFixture(t, home, "Documents")
	project := folderFixture(t, documents, "project")
	public := folderFixture(t, home, "src", "public")
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(documents, alias); err != nil {
		t.Fatal(err)
	}
	g, _ := identityGateway(t, t.TempDir(), nil)
	t.Setenv("AGENTPACK_HOME", g.options.Home)
	g.options.Packages = func(_ context.Context, dir string) (network.ProjectContents, error) {
		return network.ProjectContents{Packages: []network.ProjectPackage{{Module: filepath.Base(dir)}}}, nil
	}
	for _, dir := range []string{documents, project, public} {
		if err := os.WriteFile(paths.ManifestPath(dir), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := g.foundFolders(t.Context())
	if err != nil || len(found) != 1 || found[0].Directory != public {
		t.Fatalf("found = %+v, %v", found, err)
	}

	listing, err := g.listFolders(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Documents", "alias"} {
		if !slices.ContainsFunc(listing.Entries, func(entry folderEntry) bool { return entry.Name == name && !entry.Pack && entry.Git == "" }) {
			t.Fatalf("parent should show %s without inspecting it: %+v", name, listing.Entries)
		}
	}
	listing, err = g.listFolders(t.Context(), documents)
	if err != nil || len(listing.Row.Packages) != 1 || listing.Row.Packages[0].Module != "Documents" {
		t.Fatalf("explicit browse = %+v, %v", listing, err)
	}

	for _, dir := range []string{project, public, filepath.Join(alias, "project")} {
		if err := remember(g.options.Home, session.SessionInfo{Directory: dir, Harness: "claude", Mode: "default"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := g.projects(t.Context(), "")
	if err != nil || len(rows) != 3 {
		t.Fatalf("projects = %+v, %v", rows, err)
	}
	for _, row := range rows {
		deferred := row.Directory != public
		if !row.Available || row.DetailsDeferred != deferred || (len(row.Packages) == 0) != deferred {
			t.Fatalf("project summary = %+v", row)
		}
	}
	rows, err = g.projects(t.Context(), project)
	if err != nil || len(rows) != 1 || rows[0].DetailsDeferred || len(rows[0].Packages) != 1 {
		t.Fatalf("selected project = %+v, %v", rows, err)
	}
}

func TestMacOSProjectTopicIgnoresProtectedFolderAndLock(t *testing.T) {
	home := folderTestHome(t)
	private := folderFixture(t, home, "Documents", "project")
	public := folderFixture(t, home, "src", "public")
	linked := folderFixture(t, home, "src", "linked")
	g, _ := identityGateway(t, t.TempDir(), nil)
	for _, dir := range []string{private, public, linked} {
		if err := remember(g.options.Home, session.SessionInfo{Directory: dir, Harness: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(paths.LockPath(private), paths.LockPath(linked)); err != nil {
		t.Fatal(err)
	}
	topics := g.defaultEventTopics()
	index := slices.IndexFunc(topics, func(topic eventTopic) bool { return topic.name == "projects" })
	digest := func() string {
		t.Helper()
		value, err := topics[index].digest(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := digest()
	if err := os.WriteFile(paths.LockPath(private), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := digest(); got != before {
		t.Fatal("protected lock affected the projects topic")
	}
	if err := os.Rename(private, private+"-old"); err != nil {
		t.Fatal(err)
	}
	if got := digest(); got != before {
		t.Fatal("protected folder identity affected the projects topic")
	}
	if err := os.WriteFile(paths.LockPath(public), []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := digest(); got == before {
		t.Fatal("safe lock update was missed")
	}
}

func TestMacOSWorktreeChecksGitdirBeforeGit(t *testing.T) {
	home := folderTestHome(t)
	gitdir := folderFixture(t, home, "repo", ".git", "worktrees", "branch")
	worktree := folderFixture(t, home, "worktree")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitdir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(gitdir, "commondir")
	if err := os.WriteFile(common, []byte("../..\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if backgroundGitDeferred(worktree) {
		t.Fatal("ordinary worktree was deferred")
	}
	if err := os.WriteFile(common, []byte("../../../../Documents/repo/.git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !backgroundGitDeferred(worktree) {
		t.Fatal("worktree would use Git metadata inside Documents")
	}
}

func TestMacOSProjectWithProtectedDefinitionStaysSelectable(t *testing.T) {
	home := folderTestHome(t)
	g, _ := identityGateway(t, t.TempDir(), nil)
	t.Setenv("AGENTPACK_HOME", g.options.Home)
	dir := folderFixture(t, home, "code")
	definition := folderFixture(t, home, "Library", "Mobile Documents", "definition")
	if err := environment.SaveBinding(environment.Binding{WorkspaceRoot: dir, DefinitionRoot: definition}); err != nil {
		t.Fatal(err)
	}
	if err := remember(g.options.Home, session.SessionInfo{Directory: dir, Harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	rows, err := g.projects(t.Context(), "")
	if err != nil || len(rows) != 1 || !rows[0].Available || !rows[0].DetailsDeferred {
		t.Fatalf("bound project = %+v, %v", rows, err)
	}
}

func TestMacOSSessionGitDefersProtectedRepositoryUntilInspected(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is required")
	}
	dir := folderFixture(t, folderTestHome(t), "Documents", "repo")
	if output, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	g, _ := identityGateway(t, t.TempDir(), nil)
	info := session.SessionInfo{ID: "session-protected-git", Directory: dir, Harness: "claude"}
	if err := session.UpsertSession(t.Context(), g.store.DB, info); err != nil {
		t.Fatal(err)
	}
	status := func(query string) sessionGit {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+info.ID+"/git"+query, nil)
		r.SetPathValue("id", info.ID)
		w := httptest.NewRecorder()
		g.serveSessionGit(w, r)
		var git sessionGit
		if err := json.Unmarshal(w.Body.Bytes(), &git); err != nil || w.Code != http.StatusOK {
			t.Fatalf("git status = %d %s, %v", w.Code, w.Body.String(), err)
		}
		return git
	}
	if passive := status(""); !passive.Deferred || passive.Available {
		t.Fatalf("passive Git status = %+v", passive)
	}
	if inspected := status("?inspect=1"); inspected.Deferred || !inspected.Available {
		t.Fatalf("inspected Git status = %+v", inspected)
	}
}
