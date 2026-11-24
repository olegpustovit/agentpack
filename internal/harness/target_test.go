package harness

import (
	"slices"
	"testing"
)

func TestTargetsHaveStableOrderAndWorkspaceOverlay(t *testing.T) {
	t.Parallel()
	want := []Target{Claude, Cursor, Codex, OpenCode, Grok, Agy, OMP}
	got := AllTargets()
	if !slices.Equal(got, want) {
		t.Fatalf("AllTargets() = %v", got)
	}
	for _, target := range got {
		if target.UsesWorkspaceOverlay() != (target == Cursor || target == Agy) {
			t.Fatalf("UsesWorkspaceOverlay(%s)", target)
		}
		if parsed, ok := ParseLauncher(target.LauncherName()); !ok || parsed != target {
			t.Fatalf("launcher for %s resolved to %q", target, parsed)
		}
	}
	if _, ok := ParseLauncher("cursor"); ok {
		t.Fatal("cursor is a target name; its launcher is agent")
	}
}

func TestRawPluginSubdirsAreIndependent(t *testing.T) {
	t.Parallel()
	dirs := Claude.RawPluginSubdirs()
	dirs[0] = "modified"
	if got := Grok.RawPluginSubdirs()[0]; got != "hooks" {
		t.Fatalf("first Grok plugin subdir = %q", got)
	}
}
