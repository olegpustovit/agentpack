package staging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
)

func TestResolveCollisionsUsesOnlyTargetNativeRoots(t *testing.T) {
	for _, candidate := range registry.All() {
		t.Run(string(candidate.ID()), func(t *testing.T) {
			home, bundle := t.TempDir(), t.TempDir()
			files := []string{"skills/Code-Tutor/SKILL.md", "commands/nested/Deploy.md", "agents/nested/Reviewer.md"}
			for _, native := range []string{".claude", ".grok"} {
				t.Run(native, func(t *testing.T) {
					for _, file := range files {
						writeTree(t, filepath.Join(home, native), map[string]string{file: "user"})
						writeTree(t, bundle, map[string]string{strings.ToLower(file): "pack"})
					}
					names, err := ResolveCollisions(bundle, string(candidate.ID()), candidate.NativeUserRoots(home))
					if err != nil {
						t.Fatal(err)
					}
					wantRemoved := candidate.ID() == base.Grok || candidate.ID() == base.Claude && native == ".claude"
					_, removed := names["code-tutor"]
					if removed != wantRemoved {
						t.Fatalf("native skills = %v", names)
					}
					for _, file := range files {
						_, err := os.Stat(filepath.Join(bundle, strings.ToLower(file)))
						if wantRemoved && !os.IsNotExist(err) || !wantRemoved && err != nil {
							t.Errorf("%s: removed=%v, stat=%v", file, wantRemoved, err)
						}
						data, err := os.ReadFile(filepath.Join(home, native, file))
						if err != nil || string(data) != "user" {
							t.Fatalf("user file changed: %q, %v", data, err)
						}
					}
					if err := os.RemoveAll(filepath.Join(home, native)); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
