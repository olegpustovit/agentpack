package cache

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	githubsource "github.com/olegpustovit/agentpack/internal/github"
	"github.com/olegpustovit/agentpack/internal/lockfile"
)

const MaxPeerTarBytes = 512 << 20

type peerRestoreKey struct{}

// WithPeerRestore supplies the local gateway transport without coupling cache to worker launch code.
func WithPeerRestore(ctx context.Context, restore func(context.Context, lockfile.Package, string) error) context.Context {
	return context.WithValue(ctx, peerRestoreKey{}, restore)
}

type peerTarFS struct{ fs.ReadLinkFS }

func (files peerTarFS) ReadLink(name string) (string, error) {
	target, err := files.ReadLinkFS.ReadLink(name)
	if err != nil {
		return "", err
	}
	target = filepath.ToSlash(target)
	if !githubsource.LinkStaysInside(name, target) {
		return "", fmt.Errorf("cache link %s leaves its package", name)
	}
	return target, nil
}

func WritePeerTar(root string, out io.Writer) error {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer opened.Close()
	writer := tar.NewWriter(out)
	return errors.Join(writer.AddFS(peerTarFS{opened.FS().(fs.ReadLinkFS)}), writer.Close())
}

// ExtractPeerTar uses an OS-confined root; package integrity is checked before installation.
func ExtractPeerTar(input io.Reader, destination string) error {
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	reader := tar.NewReader(io.LimitReader(input, MaxPeerTarBytes+1))
	seen := map[string]bool{}
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !fs.ValidPath(name) || name == "." || strings.ContainsRune(name, '\\') || seen[name] {
			return errors.New("invalid or duplicate peer archive path")
		}
		seen[name] = true
		if header.Size < 0 || header.Size > MaxPeerTarBytes-total {
			return errors.New("peer package exceeds its size limit")
		}
		total += header.Size
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if !githubsource.LinkStaysInside(name, header.Linkname) {
				return errors.New("peer archive link leaves its package")
			}
			if err := root.Symlink(header.Linkname, name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
			if err := root.Chmod(name, mode); err != nil {
				return err
			}
		default:
			return errors.New("unsupported peer archive entry")
		}
	}
}
