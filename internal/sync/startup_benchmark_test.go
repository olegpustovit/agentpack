package sync

import (
	"context"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/olegpustovit/agentpack/internal/mode"
)

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
			history := filepath.Join(home, ".claude", "projects", "history.jsonl")
			if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(history, make([]byte, 32<<20), 0600); err != nil {
				b.Fatal(err)
			}
			service := NewService()
			target := harness.Codex
			if _, _, err := service.SyncForLaunch(context.Background(), home, "", harness.Codex); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if cold {
					if _, err := service.Sync(context.Background(), home, SyncOptions{Target: &target}); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, skipped, err := service.SyncForLaunch(context.Background(), home, "", harness.Codex); err != nil || !skipped {
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
