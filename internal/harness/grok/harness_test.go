package grok

import (
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparePreservesNativeSessions(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	h := New()
	if err := h.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	staged, _ := pathsHome(ctx)
	if err := os.WriteFile(filepath.Join(staged, "sessions", "thread.jsonl"), []byte("session"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".grok", "sessions", "thread.jsonl"))
	if err != nil || string(data) != "session" {
		t.Fatalf("native session=%q err=%v", data, err)
	}
}
func pathsHome(ctx base.StageContext) (string, error) {
	return StagingHome(ctx.ProjectRoot, ctx.Mode.Name())
}

func TestCredentialsSurviveRestaging(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	ctx := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	h := New()
	if err := h.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	staged, err := pathsHome(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "mcp_credentials.json"} {
		if err := os.WriteFile(filepath.Join(staged, name), []byte("credential"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pathsToReset, err := h.ResetPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range pathsToReset {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "mcp_credentials.json"} {
		data, err := os.ReadFile(filepath.Join(staged, name))
		if err != nil || string(data) != "credential" {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
}

func TestModesPreserveIndependentConfig(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	first := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	otherMode, err := mode.NewEffective("other", mode.ImplicitDefault(), nil)
	if err != nil {
		t.Fatal(err)
	}
	other := base.StageContext{ProjectRoot: project, Mode: otherMode}
	h := New()
	if err := h.Setup(first); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(first); err != nil {
		t.Fatal(err)
	}
	if err := h.Setup(other); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(first); err != nil {
		t.Fatalf("other mode overwrote first config: %v", err)
	}
	if err := h.Check(other); err != nil {
		t.Fatal(err)
	}
}
