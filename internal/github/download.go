package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/olegpustovit/agentpack/internal/paths"
)

var (
	codeloadRoot  = "https://codeload.github.com"
	retryBaseWait = 400 * time.Millisecond
)

// maxTarballBytes caps one spooled archive; a variable so tests can lower it.
var maxTarballBytes int64 = 1 << 30

type fetchDisposition uint8

const (
	fetchSuccess fetchDisposition = iota
	fetchTransient
	fetchPermanent
	fetchFatal
)

type fetchResult struct {
	disposition fetchDisposition
	reason      string
	denied      bool
	err         error
}

// DownloadTarball spools the repository archive to a file in the cache
// directory and returns its path. It tries anonymous codeload first,
// authenticated codeload for private repositories second, then the Git
// protocol; transient HTTP failures get three attempts with exponential
// backoff. The caller reads the file by path and calls release, never nil,
// when done.
func DownloadTarball(ctx context.Context, client *http.Client, owner, repo, sha string) (path string, release func(), err error) {
	scope, _ := ctx.Value(tarballReuseKey{}).(*tarballScope)
	if scope == nil {
		path, err = downloadTarball(ctx, client, owner, repo, sha)
		if err != nil {
			return "", func() {}, err
		}
		return path, func() { _ = os.Remove(path) }, nil
	}
	value, _ := scope.files.LoadOrStore(owner+"/"+repo+"@"+sha, &reusedTarball{})
	entry := value.(*reusedTarball)
	entry.Lock()
	defer entry.Unlock()
	if entry.path == "" {
		if entry.path, err = downloadTarball(ctx, client, owner, repo, sha); err != nil {
			return "", func() {}, err
		}
	}
	// The scope's cleanup removes the file.
	return entry.path, func() {}, nil
}

type tarballReuseKey struct{}

type tarballScope struct{ files sync.Map }

type reusedTarball struct {
	sync.Mutex
	path string
}

// ReuseTarballs makes DownloadTarball keep each archive it fetches for ctx: a
// resolve walks sibling paths of one repository, and each would otherwise
// download the whole repository again. cleanup removes every spooled file;
// call it once the scope's work, including on error, has ended.
func ReuseTarballs(ctx context.Context) (scoped context.Context, cleanup func()) {
	scope := &tarballScope{}
	return context.WithValue(ctx, tarballReuseKey{}, scope), func() {
		scope.files.Range(func(_, value any) bool {
			entry := value.(*reusedTarball)
			entry.Lock()
			if entry.path != "" {
				_ = os.Remove(entry.path)
				entry.path = ""
			}
			entry.Unlock()
			return true
		})
	}
}

// spoolFile creates the temporary archive file next to the cache entries, so
// extraction reads from the same filesystem it writes to.
func spoolFile() (*os.File, error) {
	root, err := paths.CacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory %s: %w", root, err)
	}
	return os.CreateTemp(root, ".tmp-archive-*.tar.gz")
}

func downloadTarball(ctx context.Context, client *http.Client, owner, repo, sha string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	type source struct {
		name  string
		token string
	}
	sources := []source{{name: "codeload-anon"}}
	if token := githubToken(); token != "" {
		sources = append(sources, source{name: "codeload-auth", token: token})
	}
	var attempts []string
	denied := true
	for _, source := range sources {
		file, err := spoolFile()
		if err != nil {
			return "", err
		}
		result := fetchCodeloadWithRetry(ctx, client, owner, repo, sha, source.token, file)
		closeErr := file.Close()
		if result.disposition == fetchSuccess && closeErr == nil {
			return file.Name(), nil
		}
		_ = os.Remove(file.Name())
		switch {
		case result.disposition == fetchSuccess:
			return "", closeErr
		case result.disposition == fetchFatal:
			return "", result.err
		}
		attempts = append(attempts, source.name+": "+result.reason)
		denied = denied && result.denied
	}
	path, err := fetchTarballWithGit(ctx, owner, repo, sha)
	if err == nil {
		return path, nil
	}
	attempts = append(attempts, "git-protocol: "+err.Error())
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	message := fmt.Sprintf("all tarball sources failed for %s/%s@%s:\n  %s", owner, repo, short, strings.Join(attempts, "\n  "))
	if denied {
		message += "\n  hint: " + accessHint(owner, repo, githubToken() != "")
	}
	return "", errors.New(message)
}

func fetchCodeloadWithRetry(ctx context.Context, client *http.Client, owner, repo, sha, token string, file *os.File) fetchResult {
	var result fetchResult
	for attempt := 0; attempt < 3; attempt++ {
		result = fetchCodeload(ctx, client, owner, repo, sha, token, file)
		if result.disposition != fetchTransient || attempt == 2 {
			return result
		}
		timer := time.NewTimer(retryBaseWait << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fetchResult{disposition: fetchFatal, err: ctx.Err()}
		case <-timer.C:
		}
	}
	return result
}

// fetchCodeload writes the archive into file, replacing what an earlier attempt left.
func fetchCodeload(ctx context.Context, client *http.Client, owner, repo, sha, token string, file *os.File) fetchResult {
	url := fmt.Sprintf("%s/%s/%s/tar.gz/%s", codeloadRoot, owner, repo, sha)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fetchResult{disposition: fetchFatal, err: err}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return fetchResult{disposition: fetchTransient, reason: "network: " + err.Error()}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if err := errors.Join(file.Truncate(0), seekStart(file)); err != nil {
			return fetchResult{disposition: fetchFatal, err: err}
		}
		written, err := io.Copy(file, io.LimitReader(response.Body, maxTarballBytes+1))
		if err != nil {
			if ctx.Err() != nil {
				return fetchResult{disposition: fetchFatal, err: ctx.Err()}
			}
			return fetchResult{disposition: fetchTransient, reason: "network: read body: " + err.Error()}
		}
		if written > maxTarballBytes {
			return fetchResult{disposition: fetchFatal, err: fmt.Errorf("GET %s: archive is larger than %d MiB", url, maxTarballBytes>>20)}
		}
		return fetchResult{disposition: fetchSuccess}
	case response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 404:
		return fetchResult{disposition: fetchPermanent, reason: response.Status, denied: true}
	case response.StatusCode == 429 || response.StatusCode >= 500:
		return fetchResult{disposition: fetchTransient, reason: response.Status}
	default:
		return fetchResult{disposition: fetchFatal, err: fmt.Errorf("GET %s -> %s", url, response.Status)}
	}
}

func seekStart(file *os.File) error {
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func fetchTarballWithGit(ctx context.Context, owner, repo, sha string) (string, error) {
	if !isFullCommit(sha) {
		return "", fmt.Errorf("invalid commit sha %q", sha)
	}
	checkout, err := os.MkdirTemp("", "agentpack-git-archive-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(checkout)
	repository, err := git.PlainInit(checkout, false)
	if err != nil {
		return "", err
	}
	remote, err := repository.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{gitRemoteURL(owner, repo)}})
	if err != nil {
		return "", err
	}
	// One commit at depth 1, never the whole history: a full clone of a large repository takes minutes.
	options := &git.FetchOptions{RefSpecs: []config.RefSpec{config.RefSpec(sha + ":refs/heads/agentpack")}, Depth: 1, Tags: git.NoTags}
	err = remote.FetchContext(ctx, options)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		if token := githubToken(); token != "" {
			options.Auth = gitAuth(token)
			err = remote.FetchContext(ctx, options)
		}
	}
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return "", fmt.Errorf("git fetch: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return "", err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(sha), Force: true}); err != nil {
		return "", fmt.Errorf("git checkout %s: %w", sha, err)
	}
	file, err := spoolFile()
	if err != nil {
		return "", err
	}
	err = errors.Join(archiveCheckout(&cappedWriter{w: file, left: maxTarballBytes}, checkout, repo+"-"+sha), file.Close())
	if err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func archiveCheckout(output io.Writer, root, prefix string) error {
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == filepath.Join(root, ".git") {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(prefix, relative))
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	return errors.Join(tarWriter.Close(), gzipWriter.Close())
}

// cappedWriter fails once more than maxTarballBytes would be written, so the
// Git-protocol fallback has the same archive cap as a codeload download.
type cappedWriter struct {
	w    io.Writer
	left int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, fmt.Errorf("archive is larger than %d MiB", maxTarballBytes>>20)
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}
