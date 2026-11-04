package acp

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkspacePath follows symlinks and ensures path stays under root or extra dirs.
func ResolveWorkspacePath(root string, extra []string, path string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("workspace root required")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	resolved, err := evalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !pathWithinRoots(resolved, root, extra) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	return resolved, nil
}

func pathWithinRoots(path, root string, extra []string) bool {
	path = filepath.Clean(path)
	bases := append([]string{root}, extra...)
	for _, base := range bases {
		if base == "" {
			continue
		}
		base, err := filepath.Abs(filepath.Clean(base))
		if err != nil {
			continue
		}
		base, err = evalSymlinks(base)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func openWorkspaceRoot(root string, extra []string, path string) (*os.Root, string, error) {
	resolved, err := ResolveWorkspacePath(root, extra, path)
	if err != nil {
		return nil, "", err
	}
	for _, base := range append([]string{root}, extra...) {
		if base == "" {
			continue
		}
		base, err = filepath.Abs(filepath.Clean(base))
		if err != nil {
			continue
		}
		base, err = evalSymlinks(base)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		opened, err := os.OpenRoot(base)
		if err != nil {
			return nil, "", err
		}
		return opened, relative, nil
	}
	return nil, "", fmt.Errorf("path escapes workspace: %s", path)
}

// ReadWorkspaceFile reads a text file confined to the workspace roots.
func ReadWorkspaceFile(root string, extra []string, path string, line, limit *int) (string, error) {
	rooted, relative, err := openWorkspaceRoot(root, extra, path)
	if err != nil {
		return "", err
	}
	defer rooted.Close()
	data, err := rooted.ReadFile(relative)
	if err != nil {
		return "", err
	}
	content := string(data)
	if line == nil && limit == nil {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 0 {
		start = *line - 1
		if start > len(lines) {
			start = len(lines)
		}
	}
	end := len(lines)
	if limit != nil && *limit > 0 && *limit < end-start {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// WriteWorkspaceFile atomically replaces a text file confined to the workspace roots.
func WriteWorkspaceFile(root string, extra []string, path, content string) error {
	rooted, relative, err := openWorkspaceRoot(root, extra, path)
	if err != nil {
		return err
	}
	defer rooted.Close()
	directory := filepath.Dir(relative)
	if err := rooted.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := rooted.Stat(relative); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporaryName := filepath.Join(directory, ".agentpack-acp-"+rand.Text())
	temporary, err := rooted.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = rooted.Remove(temporaryName)
		}
	}()
	written, writeErr := temporary.WriteString(content)
	closeErr := temporary.Close()
	if writeErr != nil {
		return writeErr
	}
	if written != len(content) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	if err := rooted.Rename(temporaryName, relative); err != nil {
		return err
	}
	renamed = true
	return nil
}

func evalSymlinks(path string) (string, error) {
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err == nil {
		return filepath.EvalSymlinks(path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolvedParent, err := evalSymlinks(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}
