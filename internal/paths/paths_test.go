package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFindProjectRootWalksAncestors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte("name = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "deep", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindProjectRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if got != want {
		t.Fatalf("FindProjectRoot() = %q, want %q", got, want)
	}
}

func TestStagingRootsAreModeSpecific(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", filepath.Join(root, "stage"))
	defaultRoot, err := StagingRootForMode(root, "default")
	if err != nil {
		t.Fatal(err)
	}
	designRoot, err := StagingRootForMode(root, "design")
	if err != nil {
		t.Fatal(err)
	}
	if defaultRoot == designRoot || !strings.Contains(designRoot, "design") {
		t.Fatalf("mode roots not distinct: default=%q design=%q", defaultRoot, designRoot)
	}
}

func TestResolveProjectRootOrCWDAcceptsExplicitEmptyProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	got, err := ResolveProjectRootOrCWD(root)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if got != want {
		t.Fatalf("ResolveProjectRootOrCWD() = %q, want %q", got, want)
	}
}

func TestModePathComponentIsStable(t *testing.T) {
	t.Parallel()
	if got, want := ModePathComponent("default"), "default"; got != want {
		t.Fatalf("ModePathComponent(default) = %q, want %q", got, want)
	}
	if got, want := ModePathComponent("Design/UI"), "design-ui-ce3f9bab"; got != want {
		t.Fatalf("ModePathComponent() = %q, want %q", got, want)
	}
}

func TestSharedStagingOverrideSeparatesProjectsAndGrokModes(t *testing.T) {
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	first, second := t.TempDir(), t.TempDir()
	a, err := StagingRootForMode(first, "default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := StagingRootForMode(second, "default")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("override shares staging between projects")
	}
	a, err = StagingGrokHomeDirForMode(first, "default")
	if err != nil {
		t.Fatal(err)
	}
	b, err = StagingGrokHomeDirForMode(first, "review")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("Grok modes share mutable configuration")
	}
}

func TestWriteAtomicReplacesFileAndLeavesNoTemporary(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "agentpack.toml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	entries, _ := os.ReadDir(directory)
	if err != nil || string(data) != "new" || len(entries) != 1 {
		t.Fatalf("data=%q err=%v entries=%v", data, err, entries)
	}
	if err := WriteAtomic(filepath.Join(directory, "missing", "file"), nil, 0o644); err == nil {
		t.Fatal("wrote into a missing directory")
	}
}

// A same-size rewrite with its modification time put back still changes the
// metadata on Unix, through the inode and status-change time.
func TestTreeMetadataSeesARewriteThatRestoresSizeAndTime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no inode or status-change time")
	}
	root := t.TempDir()
	file := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(file, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, _, err := TreeMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	// A rename swaps the inode, as an editor's atomic save does.
	if err := os.WriteFile(file+".new", []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".new", file); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if after, _, err := TreeMetadata(root); err != nil || after == before {
		t.Fatalf("rewrite with restored size and time went unseen: %v", err)
	}
}
