package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	githubsource "github.com/olegpustovit/agentpack/internal/github"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
)

// writeBenchPackages locks a realistic package set into project: four GitHub
// plugins of 50 skills with ten files each (2,000 files), already cached and
// content-hashed, so a launch reads them as it would a real project's.
func writeBenchPackages(b *testing.B, project string) {
	b.Helper()
	lock := lockfile.EmptyForProject(project)
	for index := range 4 {
		source := githubsource.Source{Owner: "acme", Repo: "pack" + strconv.Itoa(index)}
		commit := strings.Repeat(strconv.Itoa(index+1), 40)
		key := cache.ComputeKey(githubsource.NormalizedIdentity(source, commit))
		root, err := cache.EntryDir(key)
		if err != nil {
			b.Fatal(err)
		}
		write := func(relative, body string) {
			path := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		name := "pack" + strconv.Itoa(index)
		for _, manifest := range []string{".claude-plugin", ".cursor-plugin", ".codex-plugin"} {
			write(manifest+"/plugin.json", fmt.Sprintf(`{"name":%q,"version":"1.0.0","description":"bench"}`, name))
		}
		for skill := range 50 {
			directory := fmt.Sprintf("skills/%s-skill-%d/", name, skill)
			write(directory+"SKILL.md", fmt.Sprintf("---\nname: %s-skill-%d\ndescription: Benchmark skill %d\n---\n\n# Skill\n", name, skill, skill))
			for file := range 9 {
				write(fmt.Sprintf("%sreferences/note-%d.md", directory, file), strings.Repeat("reference text\n", 64))
			}
		}
		digest, err := cache.TreeDigest(root)
		if err != nil {
			b.Fatal(err)
		}
		lock.Packages = append(lock.Packages, lockfile.Package{
			Module: "github.com/acme/" + source.Repo, Direct: true, Kind: lockfile.PackagePlugin,
			URL:   githubsource.CanonicalTreeURL(githubsource.Source{Owner: "acme", Repo: source.Repo, GitRef: commit}),
			Owner: "acme", Repo: source.Repo, Commit: commit, CacheKey: key, ContentHash: digest, Name: name,
		})
	}
	if err := lock.Save(project); err != nil {
		b.Fatal(err)
	}
}

// A home workspace must not scan chat history to fingerprint configuration.
func BenchmarkMaterializationHome(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("USERPROFILE", home)
	b.Setenv("AGENTPACK_HOME", b.TempDir())
	b.Setenv("AGENTPACK_STAGING_ROOT", b.TempDir())
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	data := make([]byte, 64<<10)
	for i := range 512 {
		path := filepath.Join(home, ".claude", "projects", strconv.Itoa(i), "history.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			b.Fatal(err)
		}
	}
	if err := manifest.WriteStub(home, "demo", "0.0.1"); err != nil {
		b.Fatal(err)
	}
	writeBenchPackages(b, home)
	b.ResetTimer()
	for range b.N {
		if _, err := computeMaterializationDigest(home, home, mode.ImplicitEffective()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodexSetup(b *testing.B) {
	for _, cold := range []bool{false, true} {
		name := "warm"
		if cold {
			name = "cold"
		}
		b.Run(name, func(b *testing.B) {
			home := b.TempDir()
			b.Setenv("HOME", home)
			b.Setenv("USERPROFILE", home)
			b.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
			b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			b.Setenv("AGENTPACK_HOME", b.TempDir())
			b.Setenv("AGENTPACK_STAGING_ROOT", b.TempDir())
			if err := manifest.WriteStub(home, "demo", "0.0.1"); err != nil {
				b.Fatal(err)
			}
			if err := lockfile.Init(home, "demo", "0.0.1"); err != nil {
				b.Fatal(err)
			}
			// Warm launches carry the package set; cold keeps the empty project
			// its budget was set for.
			if !cold {
				writeBenchPackages(b, home)
			}
			history := filepath.Join(home, ".claude", "projects", "history.jsonl")
			if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(history, make([]byte, 32<<20), 0600); err != nil {
				b.Fatal(err)
			}
			service := NewService()
			ctx := cache.WithPeerRestore(context.Background(), func(context.Context, lockfile.Package, string) error {
				b.Fatal("launch setup contacted the peer cache")
				return nil
			})
			target := harness.Codex
			if _, _, err := service.SyncForLaunch(ctx, home, "", harness.Codex); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if cold {
					if _, err := service.Sync(ctx, home, SyncOptions{Target: &target}); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, skipped, err := service.SyncForLaunch(ctx, home, "", harness.Codex); err != nil || !skipped {
						b.Fatalf("skipped=%v err=%v", skipped, err)
					}
				}
			}
		})
	}
}

func BenchmarkMaterializationRealHome(b *testing.B) {
	home := os.Getenv("AGENTPACK_BENCH_HOME")
	if home == "" {
		b.Skip("set AGENTPACK_BENCH_HOME for a read-only real-profile benchmark")
	}
	b.Setenv("AGENTPACK_HOME", b.TempDir())
	b.Setenv("AGENTPACK_STAGING_ROOT", b.TempDir())
	b.ResetTimer()
	for range b.N {
		if _, err := computeMaterializationDigest(home, home, mode.ImplicitEffective()); err != nil {
			b.Fatal(err)
		}
	}
}
