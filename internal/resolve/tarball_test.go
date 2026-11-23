package resolve

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

// writeRepoArchive writes a codeload-shaped archive with two skills and a
// filler file of the given size, stored uncompressed so the archive is that large.
func writeRepoArchive(t *testing.T, repo, sha string, filler int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipWriter, _ := gzip.NewWriterLevel(file, gzip.NoCompression)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := repo + "-" + sha + "/"
	write := func(name string, size int64, content io.Reader) {
		if err := tarWriter.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(tarWriter, content, size); err != nil {
			t.Fatal(err)
		}
	}
	for _, skill := range []string{"a", "b"} {
		body := "---\nname: " + skill + "\ndescription: fixture\n---\n"
		write("skills/"+skill+"/SKILL.md", int64(len(body)), strings.NewReader(body))
	}
	write("filler.bin", filler, zeros{})
	if err := errors.Join(tarWriter.Close(), gzipWriter.Close()); err != nil {
		t.Fatal(err)
	}
	return path
}

type zeros struct{}

func (zeros) Read(buffer []byte) (int, error) { clear(buffer); return len(buffer), nil }

func spooledArchives(t *testing.T) []string {
	t.Helper()
	root, err := paths.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, ".tmp-archive-*"))
	return files
}

func TestResolveStreamsALargeArchiveOnceAndRemovesIt(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "unused")
	sha := strings.Repeat("a", 40)
	const size = 50 << 20
	archive := writeRepoArchive(t, "repo", sha, size)
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		file, err := os.Open(archive)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: file, Request: request}, nil
	})}
	project := &manifest.Manifest{Dependencies: map[string]manifest.Dependency{
		"github.com/acme/repo/skills/a": shortDependency(sha),
		"github.com/acme/repo/skills/b": shortDependency(sha),
	}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	lock, err := NewResolver(context.Background(), client).Resolve(context.Background(), t.TempDir(), project, ResolveOptions{})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Packages) != 2 || requests.Load() != 1 {
		t.Fatalf("packages = %d, archive requests = %d; want 2 packages from one download", len(lock.Packages), requests.Load())
	}
	// Cumulative allocation bounds the peak heap; holding the archive would cost its full size.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > size/4 {
		t.Fatalf("resolve allocated %d MiB for a %d MiB archive", allocated>>20, size>>20)
	}
	if leftovers := spooledArchives(t); len(leftovers) != 0 {
		t.Fatalf("resolve left %v", leftovers)
	}
}

func TestCancelledResolveLeavesNoArchives(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "unused")
	sha := strings.Repeat("b", 40)
	archive := writeRepoArchive(t, "one", sha, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/acme/one/") {
			file, err := os.Open(archive)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: closeNotifier{file, served}, Request: request}, nil
		}
		// The second repository's download is cancelled once the first archive is on disk.
		<-served
		cancel()
		return nil, context.Canceled
	})}
	project := &manifest.Manifest{Dependencies: map[string]manifest.Dependency{
		"github.com/acme/one/skills/a": shortDependency(sha),
		"github.com/acme/two/skills/a": shortDependency(sha),
	}}
	if _, err := NewResolver(ctx, client).Resolve(ctx, t.TempDir(), project, ResolveOptions{}); err == nil {
		t.Fatal("cancelled resolve succeeded")
	}
	if leftovers := spooledArchives(t); len(leftovers) != 0 {
		t.Fatalf("cancelled resolve left %v", leftovers)
	}
}

type closeNotifier struct {
	*os.File
	closed chan struct{}
}

func (body closeNotifier) Close() error {
	defer close(body.closed)
	return body.File.Close()
}
