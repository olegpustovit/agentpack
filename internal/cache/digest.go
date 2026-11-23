package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	githubsource "github.com/olegpustovit/agentpack/internal/github"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// IntegrityError reports a package tree that does not match its lock entry.
// Fetched distinguishes bytes just downloaded from an existing cache entry.
type IntegrityError struct {
	Package  lockfile.Package
	Path     string
	Expected string
	Actual   string
	Fetched  bool
}

func (err *IntegrityError) Error() string {
	if err.Fetched {
		return fmt.Sprintf("content hash mismatch for %s: the content fetched for commit %s is %s but pack.lock pins %s; nothing was cached or staged; either the source now serves different content for that commit or pack.lock was edited, so restore pack.lock from version control and do not accept the fetched content without reviewing it",
			err.name(), err.Package.Commit, err.Actual, err.Expected)
	}
	return fmt.Sprintf("content hash mismatch for %s: the cache entry %s is %s but pack.lock pins %s; nothing was staged from it; inspect the directory, then run `agentpack sync --repair` to re-fetch commit %s and verify it again",
		err.name(), err.Path, err.Actual, err.Expected, err.Package.Commit)
}

// Details lays the same facts out one per line, so the digests and the cache
// path survive a renderer that reflows error text.
func (err *IntegrityError) Details() string {
	lines := []string{"content hash mismatch: " + err.name(), "  expected  " + err.Expected, "  actual    " + err.Actual}
	if err.Fetched {
		lines = append(lines, "  source    freshly fetched commit "+err.Package.Commit, "  fix       restore pack.lock from version control; if it is intact, the source changed what it serves for this commit")
	} else {
		lines = append(lines, "  cache     "+err.Path, "  commit    "+err.Package.Commit, "  repair    agentpack sync --repair   (re-fetches the commit and verifies it again)")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Summary is the short form to show next to Details.
func (err *IntegrityError) Summary() string {
	if err.Fetched {
		return fmt.Sprintf("content hash mismatch for %s: nothing was cached or staged", err.name())
	}
	return fmt.Sprintf("content hash mismatch for %s: nothing was staged from the cache entry", err.name())
}

func (err *IntegrityError) name() string {
	if err.Package.Module != "" {
		return err.Package.Module
	}
	return err.Package.CacheKey
}

// LockEntryError reports a lock entry whose integrity fields contradict each
// other, which only happens when the entry was edited after it was locked.
type LockEntryError struct {
	Package  lockfile.Package
	Expected string
}

func (err *LockEntryError) Error() string {
	return fmt.Sprintf("pack.lock entry for %s is inconsistent: cache_key %s does not belong to commit %s (that commit has cache_key %s), so the entry was edited after it was locked; nothing was fetched or staged; restore pack.lock from version control, and move a pin with `agentpack update` or `agentpack lock --update`",
		err.Package.Module, err.Package.CacheKey, err.Package.Commit, err.Expected)
}

func (err *LockEntryError) Details() string {
	return strings.Join([]string{
		"inconsistent pack.lock entry: " + err.Package.Module,
		"  commit     " + err.Package.Commit,
		"  cache_key  " + err.Package.CacheKey,
		"  expected   " + err.Expected + "   (the cache_key of that commit)",
		"  fix        restore pack.lock from version control; move a pin with `agentpack update` or `agentpack lock --update`",
	}, "\n") + "\n"
}

func (err *LockEntryError) Summary() string {
	return fmt.Sprintf("pack.lock entry for %s was edited after it was locked: nothing was fetched or staged", err.Package.Module)
}

// CheckLockEntries verifies that every content-hashed GitHub entry still
// names the cache slot of its own repository, path and commit. agentpack
// writes the three together, so a commit that no longer matches its cache_key
// means the lock was edited by hand, and the content hash describes some
// other commit.
//
// A plugin entry with a URL but no pins was written by the Rust-era tool;
// it is refused rather than pinned again behind the user's back.
func CheckLockEntries(lock lockfile.PackLock) error {
	for _, pkg := range lock.Packages {
		if pkg.Kind == lockfile.PackagePlugin && strings.HasPrefix(pkg.URL, "https://github.com/") && (pkg.CacheKey == "" || pkg.Commit == "" || pkg.Owner == "" || pkg.Repo == "") {
			return fmt.Errorf("pack.lock entry for %s has no cache_key, commit, owner or repo; nothing was fetched or staged; run `agentpack lock` to pin it", describe(pkg))
		}
		if pkg.ContentHash == "" || isLocalPackage(pkg) || strings.HasPrefix(pkg.URL, "agentpack-local:") {
			continue
		}
		source := githubsource.Source{Owner: pkg.Owner, Repo: pkg.Repo, Path: pkg.Path}
		if expected := ComputeKey(githubsource.NormalizedIdentity(source, pkg.Commit)); pkg.CacheKey != expected {
			return &LockEntryError{Package: pkg, Expected: expected}
		}
	}
	return nil
}

// Repaired is the one-line report for a mismatch that RefetchPackage fixed.
func (err *IntegrityError) Repaired() string {
	return fmt.Sprintf("repaired %s: re-fetched commit %s into %s and verified %s (the entry was %s)",
		err.Package.Module, err.Package.Commit, err.Path, err.Expected, err.Actual)
}

const (
	readAheadFiles     = 32
	readAheadFileLimit = 1 << 20
	// Wider than the coarsest file timestamps, FAT's two seconds.
)

// TreeDigest computes the sha256-tree-v1 digest of a package directory.
//
// Every regular file and symbolic link below root is one record; directories
// and their modes are not recorded, so empty directories do not count. Records
// are ordered by slash-separated relative path, compared bytewise, and each is
// written to one SHA-256 stream as
//
//	kind 0x00 path 0x00 decimal-length 0x00 payload
//
// where kind is "f" for a regular file, "x" for a regular file with any
// execute permission bit, and "l" for a symbolic link. The payload is the file
// bytes, or the link target for a link, which is never followed. Other
// permission bits, ownership, and timestamps are ignored. Any other file type
// is an error. The result is the algorithm prefix followed by lowercase hex.
func TreeDigest(root string) (string, error) {
	return treeDigest(root, false)
}

// MatchesContentHash reports whether the tree at root, whose digest is
// actual, is the tree a lock entry pins with expected.
//
// agentpack 0.4.8 and earlier wrote every symbolic link of a GitHub archive
// as an empty file, and recorded the digest of that tree. The same package is
// now extracted with its in-package links kept, so a hash locked by those
// versions is also accepted when it equals the digest of this tree with each
// link counted as an empty file. Every file's bytes and every path are still
// covered; a new lock records the links themselves.
func MatchesContentHash(root, expected, actual string) bool {
	if actual == expected {
		return true
	}
	legacy, err := treeDigest(root, true)
	return err == nil && legacy != actual && legacy == expected
}

func treeDigest(root string, linksAsEmptyFiles bool) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect package tree %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("package tree %s is not a directory", root)
	}
	type record struct {
		relative string
		mode     fs.FileMode
		size     int64
	}
	var records []record
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
			return fmt.Errorf("unsupported file type %s at %s", info.Mode().Type(), relative)
		}
		records = append(records, record{relative: filepath.ToSlash(relative), mode: info.Mode(), size: info.Size()})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk package tree %s: %w", root, err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].relative < records[j].relative })
	// The digest is one ordered stream, but files can be read ahead of it. An
	// open is what a cold disk or a virus scanner makes slow, and those overlap.
	type loaded struct {
		data []byte
		err  error
	}
	slots := make([]chan loaded, len(records))
	for index, item := range records {
		if item.mode.IsRegular() && item.size <= readAheadFileLimit {
			slots[index] = make(chan loaded, 1)
		}
	}
	finished, window := make(chan struct{}), make(chan struct{}, readAheadFiles)
	defer close(finished)
	go func() {
		for index, slot := range slots {
			if slot == nil {
				continue
			}
			select {
			case window <- struct{}{}:
			case <-finished:
				return
			}
			go func(path string) {
				data, err := os.ReadFile(path)
				slot <- loaded{data, err}
			}(filepath.Join(root, filepath.FromSlash(records[index].relative)))
		}
	}()
	hasher := sha256.New()
	buffer := make([]byte, 32*1024)
	header := func(kind, relative string, length int64) {
		hasher.Write([]byte(kind))
		hasher.Write([]byte{0})
		hasher.Write([]byte(relative))
		hasher.Write([]byte{0})
		hasher.Write([]byte(strconv.FormatInt(length, 10)))
		hasher.Write([]byte{0})
	}
	for index, item := range records {
		path := filepath.Join(root, filepath.FromSlash(item.relative))
		if item.mode&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err == nil && linksAsEmptyFiles {
				// Only a link the extractor itself would have created can
				// stand for the empty file an older version wrote.
				if !githubsource.LinkStaysInside(item.relative, target) {
					return "", fmt.Errorf("link %s leaves the package", item.relative)
				}
				header("f", item.relative, 0)
				continue
			}
			if err != nil {
				return "", fmt.Errorf("read link %s: %w", path, err)
			}
			header("l", item.relative, int64(len(target)))
			hasher.Write([]byte(target))
			continue
		}
		kind := "f"
		if item.mode.Perm()&0o111 != 0 {
			kind = "x"
		}
		if slot := slots[index]; slot != nil {
			result := <-slot
			<-window
			if result.err != nil {
				return "", fmt.Errorf("open %s: %w", path, result.err)
			}
			if int64(len(result.data)) != item.size {
				return "", fmt.Errorf("read %s: file changed while hashing", path)
			}
			header(kind, item.relative, item.size)
			hasher.Write(result.data)
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", path, err)
		}
		header(kind, item.relative, item.size)
		// The wrapper hides os.File's WriterTo so the shared buffer is used.
		written, copyErr := io.CopyBuffer(hasher, struct{ io.Reader }{file}, buffer)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return "", fmt.Errorf("read %s: %w", path, errors.Join(copyErr, closeErr))
		}
		if written != item.size {
			return "", fmt.Errorf("read %s: file changed while hashing", path)
		}
	}
	return lockfile.ContentHashPrefix + hex.EncodeToString(hasher.Sum(nil)), nil
}

// verifiedStamp remembers that an entry's bytes matched a content hash while
// its files had this metadata. It lives outside the entry, which it describes.
type verifiedStamp struct {
	ContentHash string `json:"content_hash"`
	Metadata    string `json:"metadata"`
}

func verifiedStampPath(cacheKey string) (string, error) {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "verified", cacheKey+".json"), nil
}

// VerifyPackageIncremental is VerifyPackage for launches: an entry proved by an
// earlier full check is not read again while its metadata is unchanged. This
// is the trust the launch fast path already gives metadata.
func VerifyPackageIncremental(pkg lockfile.Package) error {
	if pkg.ContentHash == "" || pkg.CacheKey == "" {
		return nil
	}
	out, err := EntryDir(pkg.CacheKey)
	if err != nil {
		return err
	}
	if path, err := verifiedStampPath(pkg.CacheKey); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			var stamp verifiedStamp
			if json.Unmarshal(data, &stamp) == nil && stamp.ContentHash == pkg.ContentHash {
				if metadata, _, err := paths.TreeMetadata(out); err == nil && metadata == stamp.Metadata {
					return nil
				}
			}
		}
	}
	return VerifyPackage(pkg)
}

// recordVerified writes a stamp for an entry that was hashed from checkedAt on.
// A file modified about then could still change without its metadata showing
// it, as in git's racy index, so that entry gets no stamp yet. Losing a stamp
// only costs a full check, so failures are ignored.
func recordVerified(pkg lockfile.Package, root string, checkedAt time.Time) {
	path, err := verifiedStampPath(pkg.CacheKey)
	if err != nil {
		return
	}
	metadata, newest, err := paths.TreeMetadata(root)
	if err != nil || !newest.Before(checkedAt.Add(-paths.RacyTreeMetadataWindow)) {
		return
	}
	data, err := json.Marshal(verifiedStamp{ContentHash: pkg.ContentHash, Metadata: metadata})
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = paths.WriteAtomic(path, data, 0o600)
}

// VerifyPackage recomputes the cache entry digest and compares it with the
// lock. A package without a content hash is unverified and passes.
func VerifyPackage(pkg lockfile.Package) error {
	if pkg.ContentHash == "" || pkg.CacheKey == "" {
		return nil
	}
	out, err := EntryDir(pkg.CacheKey)
	if err != nil {
		return err
	}
	checkedAt := time.Now()
	actual, err := TreeDigest(out)
	if err != nil {
		return fmt.Errorf("verify %s: %w", pkg.Module, err)
	}
	if !MatchesContentHash(out, pkg.ContentHash, actual) {
		return &IntegrityError{Package: pkg, Path: out, Expected: pkg.ContentHash, Actual: actual}
	}
	recordVerified(pkg, out, checkedAt)
	return nil
}

// StatFingerprint digests the metadata of every content-hashed package's cache
// entry: each file's path, type, permission bits, size, and modification time.
// It reads no file contents, so it cannot replace VerifyPackage. It answers one
// question cheaply: has anything visibly changed since the entries were last
// verified in full?
func StatFingerprint(lock lockfile.PackLock) (string, error) {
	hasher := sha256.New()
	for _, pkg := range lock.Packages {
		if pkg.CacheKey == "" || pkg.ContentHash == "" {
			continue
		}
		out, err := EntryDir(pkg.CacheKey)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hasher, "%s\x00%s\x00", pkg.CacheKey, pkg.ContentHash)
		if info, err := os.Lstat(out); err != nil || !info.IsDir() {
			fmt.Fprint(hasher, "absent\x00")
			continue
		}
		metadata, _, err := paths.TreeMetadata(out)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hasher, "%s\x00", metadata)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
