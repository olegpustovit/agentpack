package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	githubsource "github.com/olegpustovit/agentpack/internal/github"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func BlobParentPrefixes(blobPath string) []string {
	current := path.Dir(strings.Trim(blobPath, "/"))
	var prefixes []string
	for current != "." && current != "/" {
		prefixes = append(prefixes, current)
		current = path.Dir(current)
	}
	return append(prefixes, "")
}

func MaterializeGitHubTree(ctx context.Context, client *http.Client, source githubsource.Source, displayURL string, forceRefresh bool) (lockfile.Package, error) {
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		return lockfile.Package{}, err
	}
	commit, err := githubsource.ResolveRef(ctx, client, source.Owner, source.Repo, source.GitRef, forceRefresh)
	if err != nil {
		return lockfile.Package{}, err
	}
	effective := source
	archive, release := "", func() {}
	defer func() { release() }()
	if strings.Contains(displayURL, "/blob/") && githubsource.PathLooksLikeFile(source.Path) {
		for _, prefix := range BlobParentPrefixes(source.Path) {
			candidate := source
			candidate.Path = prefix
			key := ComputeKey(githubsource.NormalizedIdentity(candidate, commit))
			out, entryErr := EntryDir(key)
			if entryErr == nil && IsPackageRoot(out) {
				effective.Path = prefix
				break
			}
		}
		if effective.Path == source.Path {
			if archive, release, err = githubsource.DownloadTarball(ctx, client, source.Owner, source.Repo, commit); err != nil {
				return lockfile.Package{}, err
			}
			var index map[string]struct{}
			if err := readArchive(archive, func(reader io.Reader) (err error) {
				index, err = githubsource.CollectRepoRelativePaths(reader)
				return err
			}); err != nil {
				return lockfile.Package{}, err
			}
			if prefix, ok := githubsource.ChoosePackagePrefix(index, source.Path, RepoDirIsPackageRoot); ok {
				effective.Path = prefix
			} else {
				effective.Path = githubsource.ParentDirInRepo(source.Path)
			}
		}
	} else {
		effective.Path = strings.Trim(source.Path, "/")
	}
	cacheKey := ComputeKey(githubsource.NormalizedIdentity(effective, commit))
	out, err := EntryDir(cacheKey)
	if err != nil {
		return lockfile.Package{}, err
	}
	displayURL = githubsource.CanonicalTreeURL(effective)
	digest := ""
	if !IsPackageRoot(out) {
		fill := func(directory string) error {
			if archive == "" {
				if archive, release, err = githubsource.DownloadTarball(ctx, client, source.Owner, source.Repo, commit); err != nil {
					return err
				}
			}
			if err := extractArchive(archive, effective.Path, directory); err != nil {
				return fmt.Errorf("%w in %s/%s archive at %.8s", err, effective.Owner, effective.Repo, commit)
			}
			if !IsPackageRoot(directory) {
				// Not a package by itself: the repository's catalog entry may define it.
				return readArchive(archive, func(reader io.Reader) error {
					_, err := writeCatalogManifest(reader, effective, directory)
					return err
				})
			}
			return nil
		}
		if digest, _, err = Install(lockfile.Package{URL: displayURL, CacheKey: cacheKey}, fill); err != nil {
			return lockfile.Package{}, err
		}
	}
	pkg, err := ClassifyMaterialized(out, displayURL, effective, commit, cacheKey)
	// Only bytes downloaded by this call may set the hash; an entry that was
	// already cached is left for the resolver to verify against the lock.
	pkg.ContentHash = digest
	return pkg, err
}

func GitHubRestore(ctx context.Context, client *http.Client) RemoteRestoreFunc {
	return func(pkg lockfile.Package, destination string) error {
		if peer, ok := ctx.Value(peerRestoreKey{}).(func(context.Context, lockfile.Package, string) error); ok && pkg.ContentHash != "" {
			if err := peer(ctx, pkg, destination); err == nil {
				if actual, err := TreeDigest(destination); err == nil && actual == pkg.ContentHash {
					return nil
				}
			}
		}
		archive, release, err := githubsource.DownloadTarball(ctx, client, pkg.Owner, pkg.Repo, pkg.Commit)
		if err != nil {
			return err
		}
		defer release()
		// Whatever a peer left is not part of the archive.
		if err := errors.Join(os.RemoveAll(destination), os.Mkdir(destination, 0o755)); err != nil {
			return err
		}
		if err := extractArchive(archive, pkg.Path, destination); err != nil {
			return err
		}
		if !IsPackageRoot(destination) {
			source := githubsource.Source{Owner: pkg.Owner, Repo: pkg.Repo, GitRef: pkg.Commit, Path: pkg.Path}
			return readArchive(archive, func(reader io.Reader) error {
				_, err := writeCatalogManifest(reader, source, destination)
				return err
			})
		}
		return nil
	}
}

// readArchive hands a spooled archive to read, which consumes it once.
func readArchive(archive string, read func(io.Reader) error) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	return read(file)
}

// extractArchive unpacks the repository path of a spooled archive into destination.
func extractArchive(archive, path, destination string) error {
	return readArchive(archive, func(reader io.Reader) error {
		written, err := githubsource.ExtractTarballWithPrefix(reader, path, destination)
		if err == nil && written == 0 && path != "" {
			err = fmt.Errorf("no files matched repository path %q", path)
		}
		return err
	})
}
