package grok

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestGrokHomeRetirementSharesStagingLeaseAndPreservesUnmirroredCredentials(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	project := t.TempDir()
	stage, err := paths.StagingRootForMode(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	homes, err := StagingHome(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, contents string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(CredentialFile(), "shared token")
	for _, name := range []string{"leased", "mirrored", "recovery"} {
		root := stage + "-g-" + name
		write(filepath.Join(root, ".staged.json"), "{}")
		write(root+".lease", "")
		token := "shared token"
		if name == "recovery" {
			token = "refreshed token"
		}
		write(filepath.Join(homes, filepath.Base(root), "auth.json"), token)
	}
	lease := flock.New(stage + "-g-leased.lease")
	if err := lease.RLock(); err != nil {
		t.Fatal(err)
	}
	defer lease.Unlock()
	paths.ReclaimStagingGenerations(project, "default", func(root string) bool {
		return RetireStagingHome(project, "default", root)
	})
	for _, name := range []string{"leased", "mirrored", "recovery"} {
		root := stage + "-g-" + name
		if _, err := os.Stat(root); os.IsNotExist(err) != (name != "leased") {
			t.Errorf("staging generation %s: %v", name, err)
		}
		auth := filepath.Join(homes, filepath.Base(root), "auth.json")
		if _, err := os.Stat(auth); os.IsNotExist(err) != (name == "mirrored") {
			t.Errorf("Grok home %s: %v", name, err)
		}
	}
}
