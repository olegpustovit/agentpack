package network

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveWorkerEndpointKeepsSupportFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"worker.json", "worker.tmp", "image.png", "environment.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeWorkerEndpoint(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker.json", "worker.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("endpoint remains: %s %v", name, err)
		}
	}
	for _, name := range []string{"image.png", "environment.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkerEndpoint(empty); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("empty endpoint remains: %v", err)
	}
}
