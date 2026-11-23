package cache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// CopyPackageDirToCache installs the source files of a local directory (see
// SourceFiles: no links, ignored files left out). The cache key and commit
// derive from those files, so an unchanged directory keeps its entry as it is.
func CopyPackageDirToCache(source, identityPrefix string) (cacheKey, commit, destination, digest string, err error) {
	if _, err := paths.EnsureUserAgentpackLayout(); err != nil {
		return "", "", "", "", err
	}
	temporary, digest, err := stage(lockfile.Package{URL: FileURL(source)}, func(directory string) (err error) {
		commit, err = hashAndCopySourceTree(source, directory)
		return err
	})
	if err != nil {
		return "", "", "", "", err
	}
	defer os.RemoveAll(temporary)
	cacheKey = ComputeKey(identityPrefix + "\x00" + commit)
	if destination, err = EntryDir(cacheKey); err != nil {
		return "", "", "", "", err
	}
	if _, err := swap(temporary, destination, digest); err != nil {
		return "", "", "", "", err
	}
	return cacheKey, commit, destination, digest, nil
}

// copyInside copies the file or directory source of root into destination,
// following links that stay inside root; os.Root refuses any other. A link
// back to a directory being copied is skipped, so a cycle cannot recurse.
func copyInside(root *os.Root, source, destination string, open []os.FileInfo) error {
	info, err := root.Stat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect copy source %s: %w", source, err)
	}
	if !info.IsDir() {
		input, err := root.Open(source)
		if err != nil {
			return fmt.Errorf("open copy source %s: %w", source, err)
		}
		defer input.Close()
		return copyReader(input, destination, info.Mode())
	}
	for _, ancestor := range open {
		if os.SameFile(ancestor, info) {
			return nil
		}
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create copy destination %s: %w", destination, err)
	}
	entries, err := fs.ReadDir(root.FS(), source)
	if err != nil {
		return fmt.Errorf("read copy source %s: %w", source, err)
	}
	for _, entry := range entries {
		if err := copyInside(root, path.Join(source, entry.Name()), filepath.Join(destination, entry.Name()), append(open, info)); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open copy source %s: %w", source, err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect copy source %s: %w", source, err)
	}
	return copyReader(input, destination, info.Mode())
}

// copyReader writes destination 0644, keeping the execute bits of mode.
func copyReader(input io.Reader, destination string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create copy parent %s: %w", filepath.Dir(destination), err)
	}
	permissions := 0o644 | mode.Perm()&0o111
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, permissions)
	if err != nil {
		return fmt.Errorf("create copy destination %s: %w", destination, err)
	}
	_, copyErr := io.Copy(output, input)
	if err := errors.Join(copyErr, output.Close(), os.Chmod(destination, permissions)); err != nil {
		return fmt.Errorf("copy to %s: %w", destination, err)
	}
	return nil
}
