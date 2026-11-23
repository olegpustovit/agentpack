package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestResolveRefUsesFullSHAAndFreshCache(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	sha := "A123456789abcdef0123456789abcdef01234567"
	got, err := ResolveRef(context.Background(), nil, "owner", "repo", sha, false)
	if err != nil || got != "a123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("full SHA = %q, %v", got, err)
	}
	if err := storeCachedRef("Owner", "Repo", "main", "b123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveRef(context.Background(), &http.Client{}, "owner", "repo", "main", false)
	if err != nil || got != "b123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("cached SHA = %q, %v", got, err)
	}
}

func TestResolveRefAndListTagsUseRESTAndPersist(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	sha := "c123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/repos/o/r/commits/main" {
			fmt.Fprintf(response, `{"sha":%q}`, sha)
			return
		}
		fmt.Fprintf(response, `[{"name":"v2.0.0","commit":{"sha":%q}},{"name":"v1.0.0","commit":{"sha":%q}}]`, sha, sha)
	}))
	defer server.Close()
	previous := apiRoot
	apiRoot = server.URL
	defer func() { apiRoot = previous }()
	got, err := ResolveRef(context.Background(), server.Client(), "o", "r", "main", false)
	if err != nil || got != sha {
		t.Fatalf("ResolveRef() = %q, %v", got, err)
	}
	tags, err := ListTags(context.Background(), server.Client(), "o", "r", false)
	if err != nil || len(tags) != 2 || tags[0].Name != "v1.0.0" {
		t.Fatalf("ListTags() = %#v, %v", tags, err)
	}
}

func TestRepoFileCachesAndFallsBack(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	calls := 0
	failing := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if failing || request.URL.Path != "/repos/o/r/contents/.claude-plugin/marketplace.json" {
			http.Error(response, "nope", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(response, `{"encoding":"base64","content":"eyJwbHVn\naW5zIjpbXX0=\n"}`)
	}))
	defer server.Close()
	previous := apiRoot
	apiRoot = server.URL
	defer func() { apiRoot = previous }()
	for range 2 {
		got, err := RepoFile(context.Background(), server.Client(), "o", "r", ".claude-plugin/marketplace.json", time.Hour)
		if err != nil || string(got) != `{"plugins":[]}` {
			t.Fatalf("RepoFile() = %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("fresh cache still fetched: %d calls", calls)
	}
	failing = true
	got, err := RepoFile(context.Background(), server.Client(), "o", "r", ".claude-plugin/marketplace.json", 0)
	if err != nil || string(got) != `{"plugins":[]}` || calls != 2 {
		t.Fatalf("stale fallback = %q, %v after %d calls", got, err, calls)
	}
}

func TestLSRemoteParsingPrefersPeeledTags(t *testing.T) {
	t.Parallel()
	base := "a123456789abcdef0123456789abcdef01234567"
	peeled := "b123456789abcdef0123456789abcdef01234567"
	output := base + "\trefs/tags/v1.0.0\n" + peeled + "\trefs/tags/v1.0.0^{}\n"
	sha, err := resolveSHAFromLSRemote(output, "v1.0.0")
	if err != nil || sha != peeled {
		t.Fatalf("resolveSHAFromLSRemote() = %q, %v", sha, err)
	}
	tags := tagPairsFromLSRemote(output)
	if len(tags) != 1 || tags[0] != (Tag{Name: "v1.0.0", SHA: peeled}) {
		t.Fatalf("tagPairsFromLSRemote() = %#v", tags)
	}
}

func TestFreshTagCacheResolvesExactTagWithoutNetwork(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	sha := "d123456789abcdef0123456789abcdef01234567"
	if err := storeCachedTags("owner", "repo", []Tag{{Name: "v1.2.3", SHA: sha}}); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRef(context.Background(), &http.Client{}, "owner", "repo", "v1.2.3", false)
	if err != nil || got != sha {
		t.Fatalf("ResolveRef() = %q, %v", got, err)
	}
}

func TestEmbeddedGitProtocolFallback(t *testing.T) {
	if os.Getenv("AGENTPACK_NETWORK_TESTS") != "1" {
		t.Skip("set AGENTPACK_NETWORK_TESTS=1 to exercise GitHub's smart HTTP endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := gitLSRemote(ctx, "olegpustovit", "agentpack", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := resolveSHAFromLSRemote(output, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	path, err := fetchTarballWithGit(ctx, "olegpustovit", "agentpack", sha)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	paths, err := CollectRepoRelativePaths(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paths["README.md"]; !ok {
		t.Fatalf("archive does not contain README.md: %v", paths)
	}
}

func TestLatestVersionFollowsThePackageFolder(t *testing.T) {
	latest := strings.Repeat("a", 40)
	older, newer := strings.Repeat("b", 40), strings.Repeat("c", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/tools/commits":
			if r.URL.Query().Get("path") != "skills/a b" {
				t.Errorf("path = %q", r.URL.Query().Get("path"))
			}
			fmt.Fprintf(w, `[{"sha":%q,"commit":{"committer":{"date":"2026-10-30T10:00:00Z"}}}]`, latest)
		case r.URL.Path == "/repos/acme/tools/compare/"+latest+"..."+older:
			fmt.Fprint(w, `{"status":"behind"}`)
		case r.URL.Path == "/repos/acme/tools/compare/"+latest+"..."+newer:
			fmt.Fprint(w, `{"status":"ahead"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	previous := apiRoot
	apiRoot = server.URL
	defer func() { apiRoot = previous }()

	got, err := LatestVersion(context.Background(), server.Client(), "acme", "tools", "skills/a b", []string{older, newer, latest, older})
	if err != nil {
		t.Fatal(err)
	}
	// A pin ahead of the folder's last change has the same files, so only the older one is behind.
	if got.Commit != latest || !got.Exact || got.CommittedAt == "" || !slices.Equal(got.Current, []string{latest, newer}) {
		t.Fatalf("latest = %+v", got)
	}
	if _, err := LatestVersion(context.Background(), server.Client(), "../evil", "tools", "", nil); err == nil {
		t.Fatal("an invalid owner must be refused")
	}
}
