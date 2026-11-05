package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyExecutableReplacesTargetWithoutChangingItsPath(t *testing.T) {
	directory := t.TempDir()
	source, destination := filepath.Join(directory, "ap.new"), filepath.Join(directory, "service")
	if err := os.WriteFile(source, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "new binary" {
		t.Fatalf("updated executable = %q, err %v", got, err)
	}
}
