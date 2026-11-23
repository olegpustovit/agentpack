package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	var prefetched []byte
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
			prefetched, err = githubsource.DownloadTarball(ctx, client, source.Owner, source.Repo, commit)
			if err != nil {
				return lockfile.Package{}, err
			}
			index, err := githubsource.CollectRepoRelativePaths(bytes.NewReader(prefetched))
			if err != nil {
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
			if len(prefetched) == 0 {
				if prefetched, err = githubsource.DownloadTarball(ctx, client, source.Owner, source.Repo, commit); err != nil {
					return err
				}
			}
			written, err := githubsource.ExtractTarballWithPrefix(bytes.NewReader(prefetched), effective.Path, directory)
			if err != nil {
				return err
			}
			if written == 0 && effective.Path != "" {
				return fmt.Errorf("no files matched repository path %q in %s/%s archive at %.8s", effective.Path, effective.Owner, effective.Repo, commit)
			}
			if !IsPackageRoot(directory) {
				// Not a package by itself: the repository's catalog entry may define it.
				_, err = writeCatalogManifest(prefetched, effective, directory)
			}
			return err
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
		data, err := githubsource.DownloadTarball(ctx, client, pkg.Owner, pkg.Repo, pkg.Commit)
		if err != nil {
			return err
		}
		// Whatever a peer left is not part of the archive.
		if err := errors.Join(os.RemoveAll(destination), os.Mkdir(destination, 0o755)); err != nil {
			return err
		}
		written, err := githubsource.ExtractTarballWithPrefix(bytes.NewReader(data), pkg.Path, destination)
		if err != nil {
			return err
		}
		if written == 0 && pkg.Path != "" {
			return fmt.Errorf("no files matched repository path %q", pkg.Path)
		}
		if !IsPackageRoot(destination) {
			source := githubsource.Source{Owner: pkg.Owner, Repo: pkg.Repo, GitRef: pkg.Commit, Path: pkg.Path}
			if _, err := writeCatalogManifest(data, source, destination); err != nil {
				return err
			}
		}
		return nil
	}
}
