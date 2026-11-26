package agy

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestPrepareWritesManifestAndAttributionRule(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "")
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	harness := New()
	if err := harness.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.Check(ctx); err != nil {
		t.Fatal(err)
	}
	root, _ := harness.Root(ctx)
	for _, relative := range []string{"plugin.json", "rules/agentpack-no-attribution.md"} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestACPLaunchConfiguresClientBypassAndUnpromptedSettle(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "agy")
	if runtime.GOOS == "windows" {
		script += ".exe"
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	project := t.TempDir()
	got, err := acpLaunch(base.LaunchContext{ProjectRoot: project, WorkspaceRoot: project, Mode: mode.ImplicitEffective()})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClientPermissionBypass {
		t.Fatal("ClientPermissionBypass = false, want true")
	}
	if got.UnpromptedUsageSettle <= 0 {
		t.Fatalf("UnpromptedUsageSettle = %v, want > 0", got.UnpromptedUsageSettle)
	}
}

func overlayContext(t *testing.T) (base.StageContext, string, string) {
	t.Helper()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	target := base.Agy
	ctx := base.StageContext{ProjectRoot: t.TempDir(), WorkspaceRoot: t.TempDir(), Mode: mode.ImplicitEffective(), LaunchTarget: &target}
	bundle, err := stagedRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(bundle, "plugin.json"), `{"name":"agentpack-bundle"}`)
	write(t, filepath.Join(bundle, "skills", "demo", "SKILL.md"), "demo")
	return ctx, bundle, filepath.Join(ctx.WorkspaceRoot, ".agents", "plugins", "agentpack-bundle")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func launchOverlay(t *testing.T, ctx base.StageContext) {
	t.Helper()
	harness := New()
	if err := harness.FinalizeWorkspaceOverlay(ctx); err != nil {
		t.Fatal(err)
	}
	if err := harness.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOverlayIsAnOwnedRealCopy(t *testing.T) {
	ctx, bundle, overlay := overlayContext(t)
	write(t, filepath.Join(bundle, "shared", "SKILL.md"), "linked")
	if err := os.Symlink(filepath.Join("..", "shared"), filepath.Join(bundle, "skills", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	script := filepath.Join(bundle, "skills", "demo", "run.sh")
	write(t, script, "#!/bin/sh\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	launchOverlay(t, ctx)

	if err := filepath.WalkDir(overlay, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type()&os.ModeSymlink != 0 {
			t.Errorf("symlink in the overlay: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := content(t, filepath.Join(overlay, "skills", "demo", "SKILL.md")) + content(t, filepath.Join(overlay, "skills", "linked", "SKILL.md")); got != "demolinked" {
		t.Fatalf("skills = %q", got)
	}
	if content(t, filepath.Join(overlay, ".gitignore")) != "*\n" || content(t, filepath.Join(overlay, OverlayMarker)) != bundle {
		t.Fatal(".gitignore or marker is wrong")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(overlay, "skills", "demo", "run.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("execute bit lost: %v", err)
		}
		if info, err := os.Stat(overlay); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("overlay is readable by others: %v", err)
		}
	}

	// A rebuild drops what the bundle dropped and clears leftovers of a killed one.
	if err := os.RemoveAll(filepath.Join(bundle, "skills", "demo")); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(ctx.WorkspaceRoot, ".agents", OverlayTempPrefix+"123")
	write(t, filepath.Join(leftover, "plugin.json"), "{}")
	launchOverlay(t, ctx)
	for _, gone := range []string{filepath.Join(overlay, "skills", "demo"), leftover} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", gone, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(ctx.WorkspaceRoot, ".agents", OverlayTempPrefix+"*")); len(left) != 0 {
		t.Fatalf("temporary directories remain: %v", left)
	}
}

func TestOverlayReplacesAnOldLinkAndKeepsItsTarget(t *testing.T) {
	ctx, bundle, overlay := overlayContext(t)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bundle, overlay); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeOverlayManifest(ctx.ProjectRoot, []string{overlay}); err != nil {
		t.Fatal(err)
	}
	if err := New().Check(ctx); err == nil {
		t.Fatal("an old link passed the check")
	}
	launchOverlay(t, ctx)
	if info, err := os.Lstat(overlay); err != nil || !info.IsDir() {
		t.Fatalf("overlay is not a directory: %v %v", info, err)
	}
	if content(t, filepath.Join(bundle, "skills", "demo", "SKILL.md")) != "demo" {
		t.Fatal("link target changed")
	}
	if _, err := os.Lstat(filepath.Join(bundle, OverlayMarker)); !os.IsNotExist(err) {
		t.Fatalf("link target was written to: %v", err)
	}
}

func TestOverlayLeavesAUsersDirectoryAlone(t *testing.T) {
	ctx, _, overlay := overlayContext(t)
	write(t, filepath.Join(overlay, "plugin.json"), `{"name":"agentpack-bundle"}`)
	write(t, filepath.Join(overlay, "mine.txt"), "mine")
	before, err := base.FingerprintFiles([]string{overlay})
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	launchOverlay(t, ctx)
	after, err := base.FingerprintFiles([]string{overlay})
	if err != nil || after != before {
		t.Fatalf("user directory changed: %v", err)
	}
	if !strings.Contains(logged.String(), "is not agentpack's") {
		t.Fatalf("no warning: %q", logged.String())
	}
	// A successful launch opens no diagnostic files.
	if recorded, _ := filepath.Glob(filepath.Join(os.Getenv("AGENTPACK_HOME"), "logs", "spool", "*")); len(recorded) != 0 {
		t.Fatalf("launch wrote diagnostics: %v", recorded)
	}
	// Once moved away, the next launch must not take the fast path.
	if err := os.RemoveAll(overlay); err != nil {
		t.Fatal(err)
	}
	if err := New().Check(ctx); err == nil {
		t.Fatal("a missing overlay passed the check")
	}
}

func TestOverlayReplacesARecordedCopyWithoutMarker(t *testing.T) {
	ctx, _, overlay := overlayContext(t)
	write(t, filepath.Join(overlay, "plugin.json"), `{"name":"agentpack-bundle"}`)
	write(t, filepath.Join(overlay, "old.txt"), "old")
	if err := writeOverlayManifest(ctx.ProjectRoot, []string{overlay}); err != nil {
		t.Fatal(err)
	}
	launchOverlay(t, ctx)
	if _, err := os.Lstat(filepath.Join(overlay, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old copy remains: %v", err)
	}
	content(t, filepath.Join(overlay, OverlayMarker))
}

func TestOverlayFollowsTheLaunchedMode(t *testing.T) {
	ctx, bundle, overlay := overlayContext(t)
	other, err := mode.NewEffective("review", mode.Definition{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := ctx
	second.Mode = other
	otherBundle, err := stagedRoot(second)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(otherBundle, "plugin.json"), `{"name":"agentpack-bundle"}`)
	write(t, filepath.Join(otherBundle, "skills", "demo", "SKILL.md"), "other")
	launchOverlay(t, ctx)
	launchOverlay(t, second)
	if err := New().Check(ctx); err == nil {
		t.Fatalf("another mode's copy passed the check of %s", bundle)
	}
	launchOverlay(t, ctx)
	if got := content(t, filepath.Join(overlay, "skills", "demo", "SKILL.md")); got != "demo" {
		t.Fatalf("SKILL.md = %q", got)
	}
}
