package cache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func cacheLeftovers(t *testing.T) []string {
	t.Helper()
	root, err := paths.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestInstallFailedFillLeavesNoSlot(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	pkg := lockfile.Package{Module: "github.com/acme/demo", CacheKey: ComputeKey("demo")}
	out, _ := EntryDir(pkg.CacheKey)
	failures := map[string]func(string) error{
		"fill error": func(directory string) error {
			writeTestFile(t, filepath.Join(directory, "SKILL.md"), "# partial")
			return errors.New("connection reset")
		},
		"not a package": func(directory string) error {
			writeTestFile(t, filepath.Join(directory, "README.md"), "# readme")
			return nil
		},
		"hash mismatch": func(directory string) error {
			writeTestFile(t, filepath.Join(directory, "SKILL.md"), "# other")
			return nil
		},
	}
	for name, fill := range failures {
		target := pkg
		if name == "hash mismatch" {
			target.ContentHash = lockfile.ContentHashPrefix + strings.Repeat("0", 64)
		}
		if _, _, err := Install(target, fill); err == nil {
			t.Fatalf("%s: install succeeded", name)
		}
		if _, err := os.Lstat(out); !os.IsNotExist(err) || len(cacheLeftovers(t)) != 0 {
			t.Fatalf("%s: slot err=%v leftovers=%v", name, err, cacheLeftovers(t))
		}
	}
}

func TestConcurrentInstallsOfOneKeyBothSucceedIdentically(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	pkg := lockfile.Package{Module: "github.com/acme/demo", CacheKey: ComputeKey("demo")}
	fill := func(directory string) error {
		for index := range 50 {
			writeTestFile(t, filepath.Join(directory, "references", strings.Repeat("n", index+1)+".md"), "note")
		}
		writeTestFile(t, filepath.Join(directory, "SKILL.md"), "# Demo")
		return nil
	}
	for range 10 {
		var group sync.WaitGroup
		digests, errs := make([]string, 2), make([]error, 2)
		for index := range 2 {
			group.Go(func() { digests[index], _, errs[index] = Install(pkg, fill) })
		}
		group.Wait()
		out, _ := EntryDir(pkg.CacheKey)
		current, err := TreeDigest(out)
		if errs[0] != nil || errs[1] != nil || err != nil || digests[0] != current || digests[1] != current {
			t.Fatalf("errs=%v digests=%v current=%s err=%v", errs, digests, current, err)
		}
	}
	if leftovers := cacheLeftovers(t); len(leftovers) != 0 {
		t.Fatalf("leftovers %v", leftovers)
	}
}

func TestUnchangedPathDependencyKeepsItsEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("inode numbers")
	}
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "SKILL.md"), "# Demo")
	_, _, first, digest, err := CopyPackageDirToCache(source, "path:"+source)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(first, "SKILL.md"))
	_, _, second, again, err := CopyPackageDirToCache(source, "path:"+source)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(second, "SKILL.md"))
	if first != second || digest != again || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged path dependency was rewritten: %s %s", first, second)
	}
}

// A path dependency keeps its scripts executable, and making one executable
// changes the entry's content hash.
func TestPathDependencyKeepsTheExecuteBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "SKILL.md"), "# Demo")
	script := filepath.Join(source, "hooks", "run.sh")
	writeTestFile(t, script, "#!/bin/sh\n")
	_, _, _, plain, err := CopyPackageDirToCache(source, "path:"+source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, out, executable, err := CopyPackageDirToCache(source, "path:"+source)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(out, "hooks", "run.sh")); err != nil || info.Mode().Perm() != 0o755 || plain == executable {
		t.Fatalf("cached script mode = %v, %v; digests %s %s", info.Mode(), err, plain, executable)
	}
}

// A path dependency restored from its source is the same tree its lock entry
// hashed: no file reached through a link, nothing the source ignores.
func TestPathDependencyRestoreUsesSourceFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX links")
	}
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	secret := t.TempDir()
	writeTestFile(t, filepath.Join(secret, "id_ed25519"), "PRIVATE KEY")
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "SKILL.md"), "# Demo")
	writeTestFile(t, filepath.Join(source, ".gitignore"), "build/\n")
	writeTestFile(t, filepath.Join(source, "build", "out.bin"), "artifact")
	if err := os.Symlink(secret, filepath.Join(source, "ssh")); err != nil {
		t.Fatal(err)
	}
	key, commit, out, digest, err := CopyPackageDirToCache(source, "path:"+source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(out); err != nil {
		t.Fatal(err)
	}
	pkg := lockfile.Package{Module: "demo", Kind: lockfile.PackageSkill, Owner: "path", URL: FileURL(source), Commit: commit, CacheKey: key, ContentHash: digest}
	if ready, err := EnsureLockCached(pkg, nil); err != nil || !ready {
		t.Fatalf("restore: ready=%v err=%v", ready, err)
	}
	for _, leaked := range []string{"ssh/id_ed25519", "build/out.bin"} {
		if _, err := os.Lstat(filepath.Join(out, filepath.FromSlash(leaked))); !os.IsNotExist(err) {
			t.Fatalf("restore copied %s", leaked)
		}
	}
}

func TestCopyInsideStopsAtCyclesAndRefusesEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX links")
	}
	source, destination := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	writeTestFile(t, filepath.Join(source, "skills", "demo", "SKILL.md"), "# Demo")
	for link, target := range map[string]string{"loop": "..", "alias": "demo"} {
		if err := os.Symlink(target, filepath.Join(source, "skills", link)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := copyInside(root, ".", destination, nil); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"skills/demo/SKILL.md", "skills/alias/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "skills", "loop", "skills")); err == nil {
		t.Fatal("the link back to the copied directory was followed")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := copyInside(root, ".", filepath.Join(t.TempDir(), "again"), nil); err == nil {
		t.Fatal("followed a link out of the root")
	}
}
