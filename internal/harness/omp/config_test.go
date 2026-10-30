package omp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestConfigInputsHonorProfileAndExcludeCredentials(t *testing.T) {
	home, workspace, custom := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PI_CONFIG_DIR", ".omp")
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	t.Setenv("PI_PROFILE", "fallback")
	t.Setenv("OMP_PROFILE", "work")
	t.Setenv("PI_CONFIG_FILES", filepath.Join(workspace, "extra.yml"))
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := ConfigInputs(workspace)
	if !slices.Contains(paths, filepath.Join(home, ".omp", "profiles", "work", "agent", "skills")) || slices.Contains(paths, filepath.Join(custom, "skills")) {
		t.Fatalf("profile inputs: %v", paths)
	}
	for _, path := range paths {
		if filepath.Base(path) == "auth.json" || filepath.Base(path) == "agent.db" {
			t.Fatalf("runtime/credential file included: %s", path)
		}
	}
	t.Setenv("OMP_PROFILE", "")
	paths = ConfigInputs(workspace)
	if !slices.Contains(paths, filepath.Join(custom, "skills")) || !slices.Contains(paths, filepath.Join(workspace, ".omp", "config.yml")) {
		t.Fatalf("default inputs: %v", paths)
	}
}
