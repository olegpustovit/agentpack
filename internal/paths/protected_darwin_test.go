package paths

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestOrphanedProjectsLeaveProtectedLocationsAlone(t *testing.T) {
	home, staging := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	private := filepath.Join(home, "Documents", "deleted")
	link := filepath.Join(home, "alias")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	for name, project := range map[string]string{
		"public": filepath.Join(home, "deleted"), "private": private, "alias": link,
	} {
		dir := filepath.Join(staging, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, projectMarker), []byte(project+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := OrphanedProjectDirectories(staging); !slices.Equal(got, []string{filepath.Join(staging, "public")}) {
		t.Fatalf("orphans = %v", got)
	}
}

func TestBackgroundLookupSkipsLinksToProtectedExecutables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "bin")+":"+filepath.Join(home, "fallback"))
	for _, name := range []string{"Documents", "bin", "fallback"} {
		if err := os.Mkdir(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	protected := filepath.Join(home, "Documents", "tool")
	fallback := filepath.Join(home, "fallback", "tool")
	for _, path := range []string{protected, fallback} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(protected, filepath.Join(home, "bin", "tool")); err != nil {
		t.Fatal(err)
	}
	if got, err := LookPathForBackground("tool"); err != nil || got != fallback {
		t.Fatalf("lookup = %q, %v", got, err)
	}
}
