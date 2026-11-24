package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapDoesNotInspectProtectedRecents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "Documents", "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	dataHome := t.TempDir()
	t.Setenv("AGENTPACK_HOME", dataHome)
	store, err := New(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bootstrap := Bootstrapper{
		Store: store, Home: dataHome, Node: "node",
		Recent: func() ([]RecentProject, error) {
			return []RecentProject{{Directory: project}}, nil
		},
		Definition: func(string, string) (string, error) {
			t.Fatal("Library tried to resolve a protected project")
			return "", nil
		},
	}
	if err := bootstrap.Run(); err != nil {
		t.Fatal(err)
	}
}
