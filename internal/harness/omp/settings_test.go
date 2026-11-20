package omp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func TestRenderOverlayNestsDottedKeys(t *testing.T) {
	data, err := RenderOverlay(map[string]json.RawMessage{
		"edit.mode":          json.RawMessage(`"patch"`),
		"edit.fuzzyMatch":    json.RawMessage(`false`),
		"compaction.percent": json.RawMessage(`-1`),
		"temperature":        json.RawMessage(`0.7`),
		"modelRoles":         json.RawMessage(`{"default":"anthropic/claude:high","smol":"x/y"}`),
		"modelRoles.smol":    json.RawMessage(`"anthropic/haiku"`),
		"cycleOrder":         json.RawMessage(`["smol","default"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "compaction:\n  percent: -1\ncycleOrder:\n- smol\n- default\nedit:\n  fuzzyMatch: false\n  mode: patch\nmodelRoles:\n  default: anthropic/claude:high\n  smol: anthropic/haiku\ntemperature: 0.7\n"
	if string(data) != want {
		t.Fatalf("overlay =\n%s\nwant\n%s", data, want)
	}
	if _, err := RenderOverlay(map[string]json.RawMessage{"a": json.RawMessage(`1`), "a.b": json.RawMessage(`2`)}); err == nil {
		t.Fatal("conflicting keys accepted")
	}
	if _, ok := RecordEntry("modelRoles.default"); !ok {
		t.Fatal("a role is not a record entry")
	}
	if _, ok := RecordEntry("edit.mode.x"); ok {
		t.Fatal("an entry of a non-record setting was accepted")
	}
}

func TestLaunchAddsOverlayOnlyWhenWritten(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OMP_PATH", os.Args[0])
	ctx := base.LaunchContext{ProjectRoot: t.TempDir()}
	overlay, err := OverlayPath()
	if err != nil {
		t.Fatal(err)
	}
	command, err := launch(ctx)
	if err != nil || slices.Contains(command.Args, "--config") {
		t.Fatalf("args without overlay = %q, %v", command.Args, err)
	}
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("edit:\n  mode: patch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	acp, err := acpLaunch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if args := acp.Cmd.Args; !slices.Equal(args[len(args)-3:], []string{"--config", overlay, "acp"}) {
		t.Fatalf("ACP args = %q", args)
	}
	ctx.Arguments = []string{"--config", overlay}
	command, err = launch(ctx)
	if err != nil || slices.Index(command.Args, overlay) != len(command.Args)-1 {
		t.Fatalf("caller's overlay duplicated: %q, %v", command.Args, err)
	}
}

func TestValidateChecksSchemaTypeWithoutOMP(t *testing.T) {
	t.Setenv("OMP_PATH", filepath.Join(t.TempDir(), "missing"))
	if err := Validate(t.Context(), "edit.mode", json.RawMessage(`"bogus"`)); err == nil {
		t.Fatal("value outside the enum accepted")
	}
	if err := Validate(t.Context(), "edit.fuzzyMatch", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	if err := Validate(t.Context(), "no.such.key", json.RawMessage(`1`)); err == nil {
		t.Fatal("unknown key accepted")
	}
}
