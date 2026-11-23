package sync

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func stagedDigest(definition, workspace string, effective mode.Effective, target *base.Target) (string, error) {
	in, err := readInputs(definition, workspace, effective, target)
	return in.staged(), err
}

// Every identity derives from one Inputs, so each input moves exactly the
// identities that read it: a launch ignores configuration it does not stage
// from, and the materialized record ignores the binary.
func TestEachInputFlipsExactlyItsIdentities(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	for _, name := range []string{"PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE", "PI_CODING_AGENT_DIR", "PI_CONFIG_FILES", "OPENCODE_PATH", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT"} {
		t.Setenv(name, "")
	}
	binary := filepath.Join(t.TempDir(), "agentpack")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(binary, "v1")
	previous := executable
	executable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { executable = previous })
	effective := mode.ImplicitEffective()
	identities := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, target := range []base.Target{base.Claude, base.OMP} {
			value, err := launchDigest(project, project, effective, target, lockfile.PackLock{})
			if err != nil {
				t.Fatal(err)
			}
			result["launch "+string(target)] = value
		}
		in, err := readInputs(project, project, effective, nil)
		if err != nil {
			t.Fatal(err)
		}
		materialized, err := in.materialized(project, nil)
		if err != nil {
			t.Fatal(err)
		}
		result["materialized"], result["synced"] = materialized, in.synced(materialized)
		return result
	}
	all := []string{"launch claude", "launch omp", "materialized", "synced"}
	for _, step := range []struct {
		name   string
		change func()
		flips  []string
	}{
		{"lock", func() { write(filepath.Join(project, "pack.lock"), "lockfile_version = 2") }, all},
		{"manifest", func() { write(filepath.Join(project, "agentpack.toml"), "name = 'x'") }, all},
		{".agents", func() { write(filepath.Join(project, ".agents", "skills", "a", "SKILL.md"), "# A") }, all},
		{"OMP config", func() { write(filepath.Join(home, ".omp", "agent", "config.yml"), "a: 1") }, []string{"launch omp", "materialized", "synced"}},
		{"binary", func() { write(binary, "v2 longer") }, []string{"launch claude", "launch omp", "synced"}},
		{"workspace CLAUDE.md", func() { write(filepath.Join(project, "CLAUDE.md"), "# Rules") }, []string{"materialized", "synced"}},
	} {
		before := identities()
		step.change()
		after := identities()
		for _, name := range all {
			want := false
			for _, flip := range step.flips {
				want = want || flip == name
			}
			if changed := before[name] != after[name]; changed != want {
				t.Errorf("%s: %s changed = %v, want %v", step.name, name, changed, want)
			}
		}
	}
}

func TestLaunchDigestTracksInputsModeAndTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	effective := mode.ImplicitEffective()
	claude, cursor := base.Claude, base.Cursor
	first, err := stagedDigest(root, root, effective, &claude)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := stagedDigest(root, root, effective, &claude)
	if first != second {
		t.Fatal("digest unstable")
	}
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _ := stagedDigest(root, root, effective, &claude)
	if changed == first {
		t.Fatal("manifest change ignored")
	}
	other, _ := stagedDigest(root, root, effective, &cursor)
	if other == changed {
		t.Fatal("target ignored")
	}
}
func TestLaunchStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	if err := WriteLaunchDigest(root, "default", base.Claude, "abc"); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadLaunchDigest(root, "default", base.Claude)
	if err != nil || !found || got != "abc" {
		t.Fatalf("got=%q found=%v err=%v", got, found, err)
	}
}

func TestOpenCodeLaunchDigestTracksSelectedBinary(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := base.OpenCode
	effective := mode.ImplicitEffective()
	t.Setenv("OPENCODE_PATH", first)
	before, err := stagedDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", second)
	after, err := stagedDigest(root, root, effective, &target)
	if err != nil || before == after {
		t.Fatalf("selected executable change ignored: %v", err)
	}
}

func TestOpenCodeLaunchDigestTracksPluginDirectoryCreation(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", config)
	target := base.OpenCode
	effective := mode.ImplicitEffective()
	before, err := stagedDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(config, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := stagedDigest(root, root, effective, &target)
	if err != nil || before == after {
		t.Fatalf("plugin directory creation ignored: %v", err)
	}
	if err := os.Mkdir(filepath.Join(config, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	withCommands, err := stagedDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "commands", "new.md"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	withCommand, err := stagedDigest(root, root, effective, &target)
	if err != nil || withCommands == withCommand {
		t.Fatalf("custom command creation ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config, "commands", "new.md"), []byte("updated fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := stagedDigest(root, root, effective, &target)
	if err != nil || updated == withCommand {
		t.Fatalf("custom command edit ignored: %v", err)
	}
}

func TestOpenCodeLaunchDigestTracksNativeConfiguration(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	target := base.OpenCode
	effective := mode.ImplicitEffective()
	digest := func() string {
		t.Helper()
		value, err := stagedDigest(root, root, effective, &target)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := digest()
	if err := os.MkdirAll(filepath.Join(config, "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "opencode", "opencode.jsonc"), []byte(`{"username":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	global := digest()
	if global == before {
		t.Fatal("global JSONC change ignored")
	}
	cli := filepath.Join(config, "opencode", "cli.json")
	if err := os.WriteFile(cli, []byte(`{"theme":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	firstCLI := digest()
	if err := os.WriteFile(cli, []byte(`{"theme":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if digest() == firstCLI {
		t.Fatal("CLI preference edit ignored")
	}
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(`{"username":"project"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := digest()
	if project == global {
		t.Fatal("project config change ignored")
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"username":"inline"}`)
	if digest() == project {
		t.Fatal("inline config change ignored")
	}
}

func TestLaunchDigestTracksExternalWorkspace(t *testing.T) {
	definition, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	target := base.Claude
	effective := mode.ImplicitEffective()
	a, err := stagedDigest(definition, first, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	b, err := stagedDigest(definition, second, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two workspaces share launch inputs")
	}
	skill := filepath.Join(first, ".agents", "skills", "local", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("workspace skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := stagedDigest(definition, first, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if changed == a {
		t.Fatal("workspace .agents change ignored")
	}
}

func TestHomeWorkspaceIgnoresRuntimeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	digest := func() string {
		t.Helper()
		value, err := computeMaterializationDigest(home, home, mode.ImplicitEffective(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := digest()
	runtimeDir := filepath.Join(home, ".cursor", "projects")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "worker.sock"))
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	history := filepath.Join(home, ".claude", "projects", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte("runtime history"), 0600); err != nil {
		t.Fatal(err)
	}
	if digest() != before {
		t.Fatal("runtime state changed configuration digest")
	}
	config := filepath.Join(home, ".cursor", "settings.json")
	if err := os.WriteFile(config, []byte(`{"editor.tabSize":4}`), 0600); err != nil {
		t.Fatal(err)
	}
	if digest() == before {
		t.Fatal("configuration change ignored")
	}
	if _, err := base.FingerprintFiles([]string{filepath.Join(runtimeDir, "worker.sock")}); err == nil {
		t.Fatal("nonregular configuration input accepted")
	}
}
