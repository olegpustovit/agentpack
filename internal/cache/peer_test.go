package cache

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
)

func TestPeerTarRoundTripPreservesDigest(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"SKILL.md": "# Peer", "bin/run": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("bin/run", filepath.Join(root, "run")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := WritePeerTar(root, &archive); err != nil {
		t.Fatal(err)
	}
	if err := ExtractPeerTar(&archive, out); err != nil {
		t.Fatal(err)
	}
	want, err := TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := TreeDigest(out)
	if err != nil || got != want {
		t.Fatalf("peer archive changed tree: %s != %s %v", got, want, err)
	}
}

func TestPeerTarRefusesUnsafeEntries(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/escape", Typeflag: tar.TypeReg},
		{Name: `dir\escape`, Typeflag: tar.TypeReg},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
		{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"},
		{Name: "device", Typeflag: tar.TypeChar},
		{Name: "huge", Typeflag: tar.TypeReg, Size: MaxPeerTarBytes + 1},
	} {
		t.Run(header.Name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			if err := writer.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
			if err := ExtractPeerTar(&data, t.TempDir()); err == nil {
				t.Fatalf("accepted unsafe entry: %+v", header)
			}
		})
	}
	root := t.TempDir()
	if err := os.Symlink("../outside", filepath.Join(root, "escape")); err != nil {
		if runtime.GOOS == "windows" {
			return
		}
		t.Fatal(err)
	}
	if err := WritePeerTar(root, io.Discard); err == nil {
		t.Fatal("served an out-of-package link")
	}
}

func TestPeerRestoreFallsBackBeforeInstallingCorruptBytes(t *testing.T) {
	for _, kind := range []string{"good", "corrupt", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			verified := t.TempDir()
			if err := os.WriteFile(filepath.Join(verified, "SKILL.md"), []byte("# Checked"), 0644); err != nil {
				t.Fatal(err)
			}
			hash, err := TreeDigest(verified)
			if err != nil {
				t.Fatal(err)
			}
			pkg := lockfile.Package{Owner: "owner", Repo: "repo", Commit: "a123456789abcdef0123456789abcdef01234567", CacheKey: ComputeKey("peer"), ContentHash: hash}
			peerCalls, githubCalls := 0, 0
			ctx := WithPeerRestore(t.Context(), func(_ context.Context, _ lockfile.Package, destination string) error {
				peerCalls++
				if kind == "unavailable" {
					return errors.New("gateway not running")
				}
				body := "# Checked"
				if kind == "corrupt" {
					body = "changed"
					if err := os.WriteFile(filepath.Join(destination, "peer-only"), []byte("discard"), 0644); err != nil {
						return err
					}
				}
				return os.WriteFile(filepath.Join(destination, "SKILL.md"), []byte(body), 0644)
			})
			archive := materializeTarball(t, map[string]string{"repo-sha/SKILL.md": "# Checked"})
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				githubCalls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(archive)), Request: r}, nil
			})}
			out := t.TempDir()
			if err := GitHubRestore(ctx, client)(pkg, out); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if kind == "good" {
				wantCalls = 0
			}
			if peerCalls != 1 || githubCalls != wantCalls {
				t.Fatalf("peer=%d GitHub=%d", peerCalls, githubCalls)
			}
			got, err := TreeDigest(out)
			if err != nil || got != hash {
				t.Fatalf("bad bytes survived fallback: %s %v", got, err)
			}
		})
	}
}

func TestPeerRestoreDoesNotRunForExistingVerifiedCache(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	pkg := lockfile.Package{CacheKey: ComputeKey("cached")}
	root, err := EntryDir(pkg.CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Cached"), 0644); err != nil {
		t.Fatal(err)
	}
	pkg.ContentHash, err = TreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPeerRestore(t.Context(), func(context.Context, lockfile.Package, string) error {
		t.Fatal("cached package contacted gateway")
		return nil
	})
	ready, err := EnsureLockCached(pkg, GitHubRestore(ctx, nil))
	if err != nil || !ready {
		t.Fatalf("verified cache: %v %v", ready, err)
	}
}
