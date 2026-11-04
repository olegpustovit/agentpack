package staging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/harness"
)

func TestVerbatimFilesAreSharedAcrossHarnessRootsButHooksAreNot(t *testing.T) {
	claude, cursor := t.TempDir(), t.TempDir()
	roots := []HarnessRoot{{Target: harness.Claude, Path: claude}, {Target: harness.Cursor, Path: cursor}}
	source := t.TempDir()
	digest, err := cache.TreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	plan := PackageTreePlan{SourceRoot: source, SourceDigest: digest}
	for _, root := range roots {
		plan.Files = append(plan.Files,
			PlannedPackageFile{Target: root.Target, SourceRelative: "assets/logo.png", RelativePath: "assets/logo.png", Contents: []byte("png"), Verbatim: true},
			PlannedPackageFile{Target: root.Target, SourceRelative: "hooks/run.sh", RelativePath: "hooks/run.sh", Contents: []byte("#!/bin/sh\n"), Verbatim: true})
	}
	if err := ApplyPackageTreePlan(plan, roots); err != nil {
		t.Fatal(err)
	}
	same := func(relative string) bool {
		a, errA := os.Stat(filepath.Join(claude, relative))
		b, errB := os.Stat(filepath.Join(cursor, relative))
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		return os.SameFile(a, b)
	}
	if !same("assets/logo.png") {
		t.Error("a verbatim asset was copied instead of shared")
	}
	if same("hooks/run.sh") {
		t.Error("a hook file is shared, so a per-harness rewrite would change both")
	}
	// A later staging write replaces the path instead of writing through the link.
	if err := writeFile(filepath.Join(claude, "assets/logo.png"), []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(cursor, "assets/logo.png")); string(data) != "png" {
		t.Fatalf("writing one root changed the other: %q", data)
	}
}
