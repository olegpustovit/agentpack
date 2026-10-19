package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopySelectedEntriesMakesReadOnlyConfigWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	source, destination := t.TempDir(), t.TempDir()
	config := filepath.Join(source, "config.toml")
	if err := os.WriteFile(config, []byte("original"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := CopySelectedEntries(source, destination, []string{"config.toml"}); err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(destination, "config.toml")
	info, err := os.Stat(copy)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged config: %v, %v", info, err)
	}
	if err := os.WriteFile(copy, []byte("updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil || string(data) != "original" {
		t.Fatalf("source changed: %q, %v", data, err)
	}
}
