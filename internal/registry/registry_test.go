package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/paths"
)

const manifestTemplate = `name = "demo"
[dependencies]
"github.com/acme/tool" = { commit = "abc123" }
[mcp.servers.local]
command = "run"
args = ["--token", "arg-secret"]
[mcp.servers.local.env]
API_TOKEN = "env-secret"
[mcp.servers.remote]
url = "https://example.test/mcp?key=url-secret"
[mcp.servers.remote.headers]
Authorization = "Bearer header-secret"
[mcp.servers.userinfo]
url = "https://user:pass@example.test/mcp"
description = "ghp_123456789012345678901234567890"
`

func lockWithCommit(commit string) []byte {
	return []byte("lockfile_version = 3\n[meta]\nname = \"demo\"\nversion = \"0.0.1\"\n[[packages]]\nmodule = \"github.com/acme/tool\"\nkind = \"skill\"\ncommit = \"" + commit + "\"\ncache_key = \"old\"\ncontent_hash = \"sha256-tree-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n")
}

func TestCaptureRedactsAndRestoresLocalBindings(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pack, first, err := store.Capture(CaptureInput{Name: "Starter", Project: "/workspace/demo", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	if !first.NeedsLocalConfiguration {
		t.Fatal("secret-bearing snapshot should need local configuration")
	}
	if strings.Contains(first.Manifest, "secret") || strings.Contains(first.Manifest, "Bearer") || strings.Contains(first.Manifest, "user:pass") || strings.Contains(first.Manifest, "ghp_123") {
		t.Fatalf("manifest retained credential text: %s", first.Manifest)
	}
	for _, secret := range []string{"env-secret", "arg-secret", "url-secret", "header-secret", "user:pass", "ghp_123"} {
		if strings.Contains(first.Manifest, secret) || strings.Contains(first.Lock, secret) {
			t.Fatalf("snapshot retained %q", secret)
		}
	}
	objects, err := os.ReadDir(filepath.Join(store.root, objectsDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		data, err := os.ReadFile(filepath.Join(store.root, objectsDir, object.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"env-secret", "arg-secret", "url-secret", "header-secret", "user:pass", "ghp_123"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("immutable object retained %q", secret)
			}
		}
	}
	if _, _, err := store.Get(pack.ID); err != nil {
		t.Fatal(err)
	}
	output, lock, err := store.restoreBytes(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"env-secret", "arg-secret", "url-secret", "header-secret", "user:pass", "ghp_123"} {
		if !strings.Contains(string(output), secret) {
			t.Fatalf("same-host restore did not retain %q", secret)
		}
	}
	if len(lock) == 0 {
		t.Fatal("missing restored lock")
	}
}

func TestCaptureRedactsSplitCredentialFlags(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--client-secret", "--credential", "--passwd", "--client_secret"} {
		secret := "raw-secret-for-" + strings.TrimLeft(flag, "-")
		manifest := []byte("name = \"demo\"\n[mcp.servers.local]\ncommand = \"run\"\nargs = [\"" + flag + "\", \"" + secret + "\"]\n")
		_, revision, err := store.Capture(CaptureInput{Project: "/workspace/" + strings.TrimLeft(flag, "-"), Manifest: manifest, Lock: lockWithCommit("abc123")})
		if err != nil {
			t.Fatalf("capture %s: %v", flag, err)
		}
		if strings.Contains(revision.Manifest, secret) {
			t.Fatalf("snapshot retained value after %s: %s", flag, revision.Manifest)
		}
	}
}

func TestRegistryRejectsUnsafePackageCacheKeys(t *testing.T) {
	for _, key := range []string{"../outside", "subdir/cache", `subdir\\cache`, ".."} {
		lock := strings.Replace(string(lockWithCommit("abc123")), `cache_key = "old"`, `cache_key = "`+key+`"`, 1)
		if _, _, err := mustStore(t).Capture(CaptureInput{Project: "/workspace/unsafe", Manifest: []byte("name = \"unsafe\"\n"), Lock: []byte(lock)}); err == nil {
			t.Fatalf("capture accepted unsafe cache key %q", key)
		}
	}
	pkg := Package{Schema: objectSchema, Module: "github.com/acme/tool", Commit: "abc123", Kind: "skill", CacheKey: "../outside"}
	data, err := marshalObject(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImported(data); err == nil {
		t.Fatal("import accepted unsafe package cache key")
	}
}

func TestCaptureRejectsUnknownLockFields(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, lock := range [][]byte{
		[]byte(strings.Replace(string(lockWithCommit("abc123")), "[meta]", "unsupported = true\n[meta]", 1)),
		[]byte(strings.Replace(string(lockWithCommit("abc123")), "kind = \"skill\"", "kind = \"skill\"\nunsupported = true", 1)),
	} {
		if _, _, err := store.Capture(CaptureInput{Project: "/workspace/invalid-lock", Manifest: []byte("name = \"demo\"\n"), Lock: lock}); err == nil {
			t.Fatal("capture accepted unsupported lock field")
		}
	}
}

func TestCaptureParentageDiffAndIndexRebuild(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	input := CaptureInput{Name: "First Name", Project: "/workspace/demo", Manifest: []byte("name = \"demo\"\n"), Lock: lockWithCommit("abc123")}
	pack, first, err := store.Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	oldIndex, err := os.ReadFile(filepath.Join(store.root, indexName))
	if err != nil {
		t.Fatal(err)
	}
	input.ID, input.Lock = pack.ID, lockWithCommit("def456")
	pack, second, err := store.Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Revisions) != 2 || len(second.ParentIDs) != 1 || second.ParentIDs[0] != first.ID {
		t.Fatalf("revision parentage: %#v", second)
	}
	if err := os.WriteFile(filepath.Join(store.root, indexName), oldIndex, 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := store.Diff(first.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Update) != 1 || diff.Update[0] != "github.com/acme/tool" || len(diff.Add)+len(diff.Remove) != 0 {
		t.Fatalf("unexpected diff: %#v", diff)
	}
	if _, err := store.Rename(pack.ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := reopened.ListAgentpacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Name != "Renamed" || len(packs[0].Revisions) != 2 {
		t.Fatalf("rebuilt index lost state: %#v", packs)
	}
	packages, err := reopened.ListPackages()
	if err != nil || len(packages) != 2 {
		t.Fatalf("rebuilt packages: %#v, %v", packages, err)
	}
}

func TestDiffTracksContentHashAndPreviewAddsIntoEmptyFolder(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstLock := lockWithCommit("abc123")
	secondLock := []byte(strings.Replace(string(firstLock), strings.Repeat("a", 64), strings.Repeat("b", 64), 1))
	pack, first, err := store.Capture(CaptureInput{Name: "Hashes", Project: "/workspace/hashes", Manifest: []byte("name = \"hashes\"\n"), Lock: firstLock})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/hashes", Manifest: []byte("name = \"hashes\"\n"), Lock: secondLock})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := store.Diff(first.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Update) != 1 || diff.Update[0] != "github.com/acme/tool" {
		t.Fatalf("content hash change missing from diff: %#v", diff)
	}
	_, preview, err := store.Preview(pack.ID, second.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Add) != 1 || preview.Add[0] != "github.com/acme/tool" {
		t.Fatalf("empty destination preview: %#v", preview)
	}
	cacheKeyLock := []byte(strings.Replace(string(secondLock), "cache_key = \"old\"", "cache_key = \"new\"", 1))
	_, third, err := store.Capture(CaptureInput{ID: pack.ID, Project: "/workspace/hashes", Manifest: []byte("name = \"hashes\"\n"), Lock: cacheKeyLock})
	if err != nil {
		t.Fatal(err)
	}
	diff, err = store.Diff(second.ID, third.ID)
	if err != nil || len(diff.Update) != 1 {
		t.Fatalf("cache key change missing from diff: %#v %v", diff, err)
	}
}

func TestCaptureDeduplicatesAndProjectSettingControlsCapture(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	manifestPath, lockPath := filepath.Join(project, "agentpack.toml"), filepath.Join(project, "pack.lock")
	if err := os.WriteFile(manifestPath, []byte("name = \"demo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, lockWithCommit("abc123"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, captured, err := CaptureProjectIfEnabled(home, project, project, "node", "default")
	if err != nil || captured {
		t.Fatalf("disabled capture: captured=%v err=%v", captured, err)
	}
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	pack, first, err := store.Capture(CaptureInput{Name: "Demo", Project: project, Manifest: []byte("name = \"demo\"\n"), Lock: lockWithCommit("abc123"), Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCapture(project, pack.ID, true); err != nil {
		t.Fatal(err)
	}
	pack, second, captured, err := CaptureProjectIfEnabled(home, project, project, "node", "default")
	if err != nil || !captured {
		t.Fatalf("enabled capture: captured=%v err=%v", captured, err)
	}
	if first.ID != second.ID || len(pack.Revisions) != 1 {
		t.Fatalf("unchanged project created another revision: %#v", pack)
	}
	if err := store.SetCapture(project, pack.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, captured, err := CaptureProjectIfEnabled(home, project, project, "node", "default"); err != nil || captured {
		t.Fatalf("disabled capture after opt out: captured=%v err=%v", captured, err)
	}
}

func TestRestoreRollsBackBothFilesWhenApplyFails(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	pack, revision, err := store.Capture(CaptureInput{Name: "Snapshot", Project: project, Manifest: []byte("name = \"saved\"\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	oldManifest, oldLock := []byte("name = \"before\"\n"), lockWithCommit("old123")
	if err := os.WriteFile(filepath.Join(project, "agentpack.toml"), oldManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "pack.lock"), oldLock, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("sync failed")
	err = store.Restore(pack.ID, revision.ID, project, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("restore error = %v", err)
	}
	gotManifest, err := os.ReadFile(filepath.Join(project, "agentpack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	gotLock, err := os.ReadFile(filepath.Join(project, "pack.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotManifest) != string(oldManifest) || string(gotLock) != string(oldLock) {
		t.Fatalf("rollback failed: manifest=%q lock=%q", gotManifest, gotLock)
	}
}

func TestRecoverInterruptedRestoreAndPreserveFileModes(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath, lockPath := filepath.Join(project, "agentpack.toml"), filepath.Join(project, "pack.lock")
	oldManifest, oldLock := []byte("name = \"before\"\n"), lockWithCommit("old123")
	for path, data := range map[string][]byte{manifestPath: oldManifest, lockPath: oldLock} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	journal := restoreJournal{Project: project, Manifest: oldManifest, Lock: oldLock, HadManifest: true, HadLock: true, ManifestMode: 0o600, LockMode: 0o600}
	if _, err := store.writeJournal(project, journal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("name = \"mixed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(project); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil || string(got) != string(oldManifest) {
		t.Fatalf("recovery manifest: %q %v", got, err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery mode: %v %v", info, err)
	}
}

func TestRecoverRejectsIncompleteJournalWithoutChangingFiles(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(project, "agentpack.toml")
	before := []byte("name = \"mixed\"\n")
	if err := os.WriteFile(manifestPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := paths.ProjectPathHash(project)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(store.root, transactionsDir, hash+".json")
	if err := writeAtomic(journalPath, []byte(`{"project":"`+project+`","manifest":"bmFtZSA9IFwiYmVmb3JlXCIK","lock":null,"manifest_mode":384,"lock_mode":0,"had_lock":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(project); err == nil {
		t.Fatal("recovery accepted incomplete journal")
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("incomplete journal changed project file: %q %v", after, err)
	}
}

func TestPendingRestoreIsReadOnly(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := PendingRestore(home, project)
	if err != nil || pending {
		t.Fatalf("unexpected pending restore: %v %v", pending, err)
	}
	manifestPath := filepath.Join(project, "agentpack.toml")
	before := []byte("name = \"mixed\"\n")
	if err := os.WriteFile(manifestPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{Project: project, Manifest: []byte("name = \"before\"\n"), HadManifest: true, ManifestMode: 0o600}
	journalPath, err := store.writeJournal(project, journal)
	if err != nil {
		t.Fatal(err)
	}
	pending, err = PendingRestore(home, project)
	if err != nil || !pending {
		t.Fatalf("pending restore not reported: %v %v", pending, err)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("read-only probe changed journal: %v", err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("read-only probe changed manifest: %q %v", after, err)
	}
}

func TestReadRejectsCorruptObjectAndDisabledCaptureDoesNotCreateRegistry(t *testing.T) {
	home := t.TempDir()
	_, _, _, err := CaptureProjectIfEnabled(home, t.TempDir(), "/source", "node", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "registry")); !os.IsNotExist(err) {
		t.Fatalf("disabled capture created registry: %v", err)
	}
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	pack, revision, err := store.Capture(CaptureInput{Name: "One", Project: "/workspace/one", Manifest: []byte("name = \"one\"\n"), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.root, objectsDir, revision.ID+".json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(pack.ID); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("corrupt object accepted: %v", err)
	}
}

func TestRestoreWithoutLocalBindingsRequiresConfiguration(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := store.Capture(CaptureInput{Name: "Secret", Project: "/workspace/secret", Manifest: []byte(manifestTemplate), Lock: lockWithCommit("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	bindingPath := filepath.Join(store.root, bindingsDir, revision.ID+".json")
	data, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	var local bindings
	if err := json.Unmarshal(data, &local); err != nil {
		t.Fatal(err)
	}
	for path := range local.Values {
		delete(local.Values, path)
		break
	}
	data, err = json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.restoreBytes(revision); err == nil || !strings.Contains(err.Error(), "needs local configuration") {
		t.Fatalf("missing bindings were ignored: %v", err)
	}
}
