package grok

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// After Sign out, neither the shared login nor one made by running grok directly counts.
func TestLogoutOutranksADirectLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTPACK_HOME", filepath.Join(home, "ap"))
	t.Setenv("GROK_PATH", os.Args[0])
	native := filepath.Join(home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte(`{"x":{"key":"k"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Account(context.Background()).SignedIn {
		t.Fatal("a direct login is not signed in")
	}
	if err := Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if Account(context.Background()).SignedIn {
		t.Fatal("still signed in after Sign out")
	}
}
