package sync

import (
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLaunchDigestTracksInputsModeAndTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	effective := mode.ImplicitEffective()
	claude, cursor := base.Claude, base.Cursor
	first, err := ComputeLaunchDigest(root, root, effective, &claude)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := ComputeLaunchDigest(root, root, effective, &claude)
	if first != second {
		t.Fatal("digest unstable")
	}
	if err := os.WriteFile(filepath.Join(root, "agentpack.toml"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _ := ComputeLaunchDigest(root, root, effective, &claude)
	if changed == first {
		t.Fatal("manifest change ignored")
	}
	other, _ := ComputeLaunchDigest(root, root, effective, &cursor)
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
	before, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PATH", second)
	after, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil || before == after {
		t.Fatalf("selected executable change ignored: %v", err)
	}
}

func TestOpenCodeLaunchDigestTracksPluginDirectoryCreation(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", config)
	target := base.OpenCode
	effective := mode.ImplicitEffective()
	before, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(config, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil || before == after {
		t.Fatalf("plugin directory creation ignored: %v", err)
	}
	if err := os.Mkdir(filepath.Join(config, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	withCommands, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "commands", "new.md"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	withCommand, err := ComputeLaunchDigest(root, root, effective, &target)
	if err != nil || withCommands == withCommand {
		t.Fatalf("custom command creation ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(config, "commands", "new.md"), []byte("updated fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := ComputeLaunchDigest(root, root, effective, &target)
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
		value, err := ComputeLaunchDigest(root, root, effective, &target)
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
	a, err := ComputeLaunchDigest(definition, first, effective, &target)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComputeLaunchDigest(definition, second, effective, &target)
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
	changed, err := ComputeLaunchDigest(definition, first, effective, &target)
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
		value, err := computeMaterializationDigest(home, home, mode.ImplicitEffective())
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
	if _, err := fingerprintInputs([]string{filepath.Join(runtimeDir, "worker.sock")}); err == nil {
		t.Fatal("nonregular configuration input accepted")
	}
}
