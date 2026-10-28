package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveContextSplitsDefinitionAndWorkspace(t *testing.T) {
	t.Parallel()
	definition := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(definition, ManifestName), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, err := ResolveContext("", definition, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	wantDef, _ := filepath.EvalSymlinks(definition)
	wantWS, _ := filepath.EvalSymlinks(workspace)
	if ctx.DefinitionRoot != wantDef || ctx.WorkspaceRoot != wantWS {
		t.Fatalf("context = %#v", ctx)
	}
}

func TestResolveContextLegacyProjectRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, err := ResolveContext(root, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	if ctx.DefinitionRoot != want || ctx.WorkspaceRoot != want {
		t.Fatalf("context = %#v", ctx)
	}
}

func TestResolveContextKeepsWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project1")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ManifestPath(root), []byte("name = \"workspace\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	root, _ = filepath.EvalSymlinks(root)
	workspace, _ = filepath.EvalSymlinks(workspace)
	for _, test := range []struct {
		name, project, definition, workspace, bound string
		want                                        Context
	}{
		{"discover parent", "", "", "", "", Context{root, workspace}},
		{"explicit workspace", "", "", workspace, "", Context{root, workspace}},
		{"explicit definition", "", root, "", "", Context{root, workspace}},
		{"bound definition", "", "", "", root, Context{root, workspace}},
		{"legacy root", root, "", "", "", Context{root, root}},
		{"workspace overrides legacy root", root, "", workspace, "", Context{root, workspace}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, resolve := range []func(string, string, string, string) (Context, error){ResolveContext, ResolveContextOrCWD} {
				got, err := resolve(test.project, test.definition, test.workspace, test.bound)
				if err != nil || got != test.want {
					t.Fatalf("context = %+v, %v; want %+v", got, err, test.want)
				}
			}
		})
	}
	if err := os.Remove(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveContextOrCWD("", "", "", "")
	if err != nil || got != (Context{workspace, workspace}) {
		t.Fatalf("without a manifest: %+v, %v", got, err)
	}
}
