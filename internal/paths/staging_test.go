package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
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
	if published, err := stagingGenerations(legacy).Current(); err != nil || published != "" {
		t.Fatalf("unverified generation was published: %s, %v", published, err)
	}
	marker := filepath.Join(first.Root(), "session")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	if published, err := stagingGenerations(legacy).Current(); err != nil || published != first.Root() {
		t.Fatalf("published generation: %s, %v", published, err)
	}
	second, err := BeginStagingGeneration(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	second.Abort()
	if selected, err := StagingRootForMode(project, "default"); err != nil || selected != first.Root() {
		t.Fatalf("failed rebuild changed selected root: %s, %v", selected, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("session data changed: %q, %v", data, err)
	}
	// An external publisher does not change paths retained by this process.
	if err := os.WriteFile(legacy+".current", []byte(filepath.Base(second.Root())), 0o600); err != nil {
		t.Fatal(err)
	}
	if selected, _ := StagingRootForMode(project, "default"); selected != first.Root() {
		t.Fatalf("reader switched under a running session: %s", selected)
	}
}

func TestStagingGenerationRefusesEscapingPointer(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	legacy, err := stagingModePath(project, "default", true)
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

func TestStagingSelectionSurvivesPublishAndReclaimBeforeLease(t *testing.T) {
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	project := t.TempDir()
	legacy, err := stagingModePath(project, "default", true)
	if err != nil {
		t.Fatal(err)
	}
	first, second := legacy+"-g-first", legacy+"-g-second"
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".staged.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteAtomic(legacy+".current", []byte(filepath.Base(first)), 0o600); err != nil {
		t.Fatal(err)
	}
	set := stagingGenerations(legacy)
	selected, lease, err := set.lease(func() (string, error) {
		root, err := set.Current()
		if err != nil {
			return "", err
		}
		// Simulate a publisher and reclaimer running after the pointer read but
		// before the selecting process has acquired its generation lease.
		if err := WriteAtomic(legacy+".current", []byte(filepath.Base(second)), 0o600); err != nil {
			return "", err
		}
		ReclaimStagingGenerations(project, "default", nil)
		return root, nil
	})
	if err != nil || lease == nil || selected != first {
		t.Fatalf("select first generation: %q, %v", selected, err)
	}
	defer lease.Unlock()
	ReclaimStagingGenerations(project, "default", nil)
	if _, err := os.Stat(filepath.Join(selected, ".staged.json")); err != nil {
		t.Fatalf("selected generation was reclaimed: %v", err)
	}
	if err := lease.Unlock(); err != nil {
		t.Fatal(err)
	}
	ReclaimStagingGenerations(project, "default", nil)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("released generation was not reclaimed: %v", err)
	}
}

func TestReclaimKeepsLeasedRecordedAndUnfinishedGenerations(t *testing.T) {
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	project := t.TempDir()
	published, err := BeginStagingGeneration(project, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := published.Commit(); err != nil {
		t.Fatal(err)
	}
	legacy, err := stagingModePath(project, "default", true)
	if err != nil {
		t.Fatal(err)
	}
	generation := func(name string, built bool) string {
		root := legacy + "-g-" + name
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+generationLeaseSuffix, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if built {
			if err := os.WriteFile(filepath.Join(root, ".staged.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	dead, held, recorded, unfinished := generation("dead", true), generation("held", true), generation("recorded", true), generation("unfinished", false)
	// Another mode can share this mode's generation prefix.
	otherLegacy, err := stagingModePath(project, "default-g-other", true)
	if err != nil {
		t.Fatal(err)
	}
	otherMode, err := stagingGenerations(otherLegacy).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer otherMode.Abort()
	if err := os.WriteFile(filepath.Join(otherMode.Root(), ".staged.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := otherMode.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := otherMode.Abort(); err != nil {
		t.Fatal(err)
	}
	// Another process's lease: a separate lock on the same file.
	lease := flock.New(held + ".lease")
	if err := lease.RLock(); err != nil {
		t.Fatal(err)
	}
	defer lease.Unlock()
	ReclaimStagingGenerations(project, "default", func(root string) bool {
		if root == dead {
			set := stagingGenerations(legacy)
			if lease, err := set.Lease(root); err != nil || lease != nil {
				t.Errorf("reclaimer did not exclude a remembered selection: %v, %v", lease, err)
				if lease != nil {
					_ = lease.Unlock()
				}
			}
		}
		return root != recorded
	})
	for root, want := range map[string]bool{dead: false, held: true, recorded: true, unfinished: true, published.Root(): true, otherMode.Root(): true} {
		if _, err := os.Stat(root); (err == nil) != want {
			t.Errorf("%s: kept=%v, want %v", filepath.Base(root), err == nil, want)
		}
	}
}

func TestReclaimDoesNotRecreateLeaseDuringProjectRemoval(t *testing.T) {
	directory := t.TempDir()
	set := GenerationSet{Directory: directory, Prefix: "g-", Pointer: filepath.Join(directory, "current")}
	generation, err := set.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := generation.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := generation.Abort(); err != nil {
		t.Fatal(err)
	}
	// Stop teardown after it removes the pointer and lease, before it removes the root.
	leasePath := generation.Root() + generationLeaseSuffix
	for _, path := range []string{set.Pointer, leasePath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	set.Reclaim(func(root string) bool {
		t.Errorf("reclaimer claimed generation after its lease was removed: %s", root)
		return false
	})
	if _, err := os.Lstat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("reclaimer recreated the lease: %v", err)
	}
}

func TestStagingGenerationRefusesSymlinkRoot(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	root, err := stagingRootBase(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Skip(err)
	}
	if _, err := BeginStagingGeneration(project, "default"); err == nil {
		t.Fatal("accepted a symlink as the staging root")
	}
}

func TestStagingGenerationRefusesPublicRoot(t *testing.T) {
	if os.Geteuid() < 0 {
		t.Skip("Unix mode check")
	}
	for _, test := range []struct {
		mode   os.FileMode
		accept bool
	}{
		{0o755, true},  // an earlier release's root: tightened to 0700 and used
		{0o777, false}, // others could have written into it
		{0o775, false},
	} {
		project := t.TempDir()
		t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
		root, err := stagingRootBase(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, test.mode); err != nil {
			t.Fatal(err)
		}
		_, err = BeginStagingGeneration(project, "default")
		if test.accept != (err == nil) {
			t.Fatalf("mode %o: err = %v, want accept=%v", test.mode, err, test.accept)
		}
		if info, statErr := os.Stat(root); test.accept && (statErr != nil || info.Mode().Perm() != 0o700) {
			t.Fatalf("mode %o was not tightened: %v %v", test.mode, info.Mode().Perm(), statErr)
		}
	}
}
