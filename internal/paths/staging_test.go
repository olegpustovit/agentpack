package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStagingGenerationPublishesOnlyOnSuccessAndPinsReaders(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	legacy, err := StagingRootForMode(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	first, err := BeginStagingGeneration(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Abort()
	if published, err := readStagingSelection(legacy); err != nil || published != legacy {
		t.Fatalf("unverified generation was published: %s, %v", published, err)
	}
	marker := filepath.Join(first.Root, "session")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Publish(); err != nil {
		t.Fatal(err)
	}
	if published, err := readStagingSelection(legacy); err != nil || published != first.Root {
		t.Fatalf("published generation: %s, %v", published, err)
	}
	second, err := BeginStagingGeneration(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	second.Abort()
	if selected, err := StagingRootForMode(project, "default"); err != nil || selected != first.Root {
		t.Fatalf("failed rebuild changed selected root: %s, %v", selected, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("session data changed: %q, %v", data, err)
	}
	// An external publisher does not change paths retained by this process.
	if err := os.WriteFile(legacy+".current", []byte(filepath.Base(second.Root)), 0o600); err != nil {
		t.Fatal(err)
	}
	if selected, _ := StagingRootForMode(project, "default"); selected != first.Root {
		t.Fatalf("reader switched under a running session: %s", selected)
	}
}

func TestStagingGenerationRefusesEscapingPointer(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	legacy, err := stagingModePath(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy+".current", []byte("../outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StagingRootForMode(project, "default"); err == nil {
		t.Fatal("accepted a staging pointer outside the mode")
	}
}
