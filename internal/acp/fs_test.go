package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorkspacePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "secret.txt")
	if _, err := ResolveWorkspacePath(root, nil, viaLink); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestReadWriteWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b.txt")
	if err := WriteWorkspaceFile(root, nil, path, "hi"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWorkspaceFile(root, nil, path, nil, nil)
	if err != nil || got != "hi" {
		t.Fatalf("ReadWorkspaceFile() = %q, %v", got, err)
	}

	extra := t.TempDir()
	extraPath := filepath.Join(extra, "extra.txt")
	if err := WriteWorkspaceFile(root, []string{extra}, extraPath, "extra"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadWorkspaceFile(root, []string{extra}, extraPath, nil, nil)
	if err != nil || got != "extra" {
		t.Fatalf("ReadWorkspaceFile(extra) = %q, %v", got, err)
	}
	link := filepath.Join(root, "extra-link")
	if err := os.Symlink(extra, link); err != nil {
		t.Fatal(err)
	}
	linkedPath := filepath.Join(link, "linked.txt")
	if err := WriteWorkspaceFile(root, []string{extra}, linkedPath, "linked"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadWorkspaceFile(root, []string{extra}, linkedPath, nil, nil)
	if err != nil || got != "linked" {
		t.Fatalf("ReadWorkspaceFile(extra symlink) = %q, %v", got, err)
	}
}
func TestReadWorkspaceFileLargeLimitDoesNotOverflow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}
	line, limit := 2, int(^uint(0)>>1)
	got, err := ReadWorkspaceFile(root, nil, path, &line, &limit)
	if err != nil || got != "two\nthree" {
		t.Fatalf("ReadWorkspaceFile(large limit) = %q, %v", got, err)
	}
}

func TestWorkspaceFileOperationsRejectSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "secret.txt")
	if _, err := ReadWorkspaceFile(root, nil, viaLink, nil, nil); err == nil {
		t.Fatal("expected read escape error")
	}
	if err := WriteWorkspaceFile(root, nil, viaLink, "changed"); err == nil {
		t.Fatal("expected write escape error")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "original" {
		t.Fatalf("outside target = %q, %v", got, err)
	}
}

func TestWriteWorkspaceFileAtomicReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkspaceFile(root, nil, path, "replacement"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement = %q, %v", got, err)
	}

	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(blocked, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkspaceFile(root, nil, blocked, "replacement"); err == nil {
		t.Fatal("expected replacement error")
	}
	got, err = os.ReadFile(sentinel)
	if err != nil || string(got) != "keep" {
		t.Fatalf("failed replacement changed original: %q, %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".agentpack-acp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}
