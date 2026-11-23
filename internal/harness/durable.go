package harness

import (
	"fmt"
	"os"
	"path/filepath"
)

func LinkDurableDirectory(native, staged string) error {
	if err := os.MkdirAll(native, 0o755); err != nil {
		return err
	}
	if err := removeAny(staged); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return err
	}
	if err := linkDirectory(native, staged); err != nil {
		return fmt.Errorf("create durable directory link %s: %w", staged, err)
	}
	return nil
}

func LinkDurableFile(native, staged string) error {
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(native, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := removeAny(staged); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return err
	}
	if err := os.Link(native, staged); err == nil {
		return nil
	}
	if err := os.Symlink(native, staged); err != nil {
		return fmt.Errorf("create durable file link %s: %w", staged, err)
	}
	return nil
}

func DurablePathMatches(path, target string) bool {
	left, leftErr := os.Stat(path)
	right, rightErr := os.Stat(target)
	return leftErr == nil && rightErr == nil && os.SameFile(left, right)
}

func removeAny(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}
