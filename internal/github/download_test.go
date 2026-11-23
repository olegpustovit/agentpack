package github

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestDownloadTarballRetriesTransientCodeload(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = response.Write([]byte("archive"))
	}))
	defer server.Close()
	previousRoot, previousWait := codeloadRoot, retryBaseWait
	codeloadRoot, retryBaseWait = server.URL, time.Millisecond
	defer func() { codeloadRoot, retryBaseWait = previousRoot, previousWait }()
	data, err := downloadText(t, context.Background(), server.Client(), "owner", "repo", "a123456789abcdef0123456789abcdef01234567")
	if err != nil || data != "archive" || calls.Load() != 3 {
		t.Fatalf("DownloadTarball() = %q, %v; calls=%d", data, err, calls.Load())
	}
	if leftovers := spooled(t); len(leftovers) != 0 {
		t.Fatalf("released archive left %v", leftovers)
	}
}

func TestFetchCodeloadClassifiesStatuses(t *testing.T) {
	for status, disposition := range map[int]fetchDisposition{
		http.StatusUnauthorized:        fetchPermanent,
		http.StatusNotFound:            fetchPermanent,
		http.StatusTooManyRequests:     fetchTransient,
		http.StatusInternalServerError: fetchTransient,
		http.StatusTeapot:              fetchFatal,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "archive")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(status) }))
			defer server.Close()
			previous := codeloadRoot
			codeloadRoot = server.URL
			defer func() { codeloadRoot = previous }()
			result := fetchCodeload(context.Background(), server.Client(), "o", "r", "sha", "", file)
			if result.disposition != disposition {
				t.Fatalf("status %d disposition = %d, want %d", status, result.disposition, disposition)
			}
		})
	}
}

func TestArchiveCheckoutProducesExtractableGitHubShape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skill", "SKILL.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := archiveCheckout(&archive, root, "repo-deadbeef"); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := ExtractTarballWithPrefix(&archive, "", destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "skill", "SKILL.md"))
	if err != nil || string(data) != "# fixture\n" {
		t.Fatalf("extracted=%q err=%v", data, err)
	}
}

// downloadText downloads an archive, returns its contents and releases it.
func downloadText(t *testing.T, ctx context.Context, client *http.Client, owner, repo, sha string) (string, error) {
	t.Helper()
	path, release, err := DownloadTarball(ctx, client, owner, repo, sha)
	if err != nil {
		return "", err
	}
	defer release()
	data, err := os.ReadFile(path)
	return string(data), err
}

// spooled lists the archive files left in the cache directory.
func spooled(t *testing.T) []string {
	t.Helper()
	root, err := paths.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, ".tmp-archive-*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func codeloadServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	stubGH(t, "")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := codeloadRoot
	codeloadRoot = server.URL
	t.Cleanup(func() { codeloadRoot = previous })
	return server
}

func TestDownloadTarballRefusesAnOversizedArchiveAndRemovesIt(t *testing.T) {
	server := codeloadServer(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(bytes.Repeat([]byte("x"), 2048))
	})
	previous := maxTarballBytes
	maxTarballBytes = 1024
	defer func() { maxTarballBytes = previous }()
	_, _, err := DownloadTarball(context.Background(), server.Client(), "o", "r", "a123456789abcdef0123456789abcdef01234567")
	if err == nil || !strings.Contains(err.Error(), "archive is larger than") {
		t.Fatalf("DownloadTarball error = %v, want size refusal", err)
	}
	if leftovers := spooled(t); len(leftovers) != 0 {
		t.Fatalf("oversized archive left %v", leftovers)
	}
}

func TestReuseTarballsReadsOneFileAndRemovesItAtTheEnd(t *testing.T) {
	var requests atomic.Int32
	server := codeloadServer(t, func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = response.Write([]byte("archive"))
	})
	const sha = "a123456789abcdef0123456789abcdef01234567"
	ctx, cleanup := ReuseTarballs(context.Background())
	first, release, err := DownloadTarball(ctx, server.Client(), "o", "r", sha)
	if err != nil {
		t.Fatal(err)
	}
	release()
	second, _, err := DownloadTarball(ctx, server.Client(), "o", "r", sha)
	if err != nil || second != first || requests.Load() != 1 {
		t.Fatalf("second download = %q, %v, requests=%d; want %q from one request", second, err, requests.Load(), first)
	}
	for range 2 {
		if data, err := os.ReadFile(first); err != nil || string(data) != "archive" {
			t.Fatalf("reused archive = %q, %v", data, err)
		}
	}
	cleanup()
	if leftovers := spooled(t); len(leftovers) != 0 {
		t.Fatalf("cleanup left %v", leftovers)
	}
	if data, err := downloadText(t, context.Background(), server.Client(), "o", "r", sha); err != nil || data != "archive" || requests.Load() != 2 {
		t.Fatalf("unscoped download = %q, %v, requests=%d; want one request per scope", data, err, requests.Load())
	}
}

func TestDownloadTarballCancelledMidBodyLeavesNoFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := codeloadServer(t, func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write(bytes.Repeat([]byte("x"), 4096))
		response.(http.Flusher).Flush()
		cancel()
		<-request.Context().Done()
	})
	_, _, err := DownloadTarball(ctx, server.Client(), "o", "r", "a123456789abcdef0123456789abcdef01234567")
	if err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if leftovers := spooled(t); len(leftovers) != 0 {
		t.Fatalf("cancelled download left %v", leftovers)
	}
}

func TestCappedWriterRefusesAnArchivePastTheCap(t *testing.T) {
	var out bytes.Buffer
	writer := &cappedWriter{w: &out, left: 4}
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("de")); err == nil {
		t.Fatal("wrote past the cap")
	}
	if out.String() != "abc" {
		t.Fatalf("wrote %q", out.String())
	}
}
