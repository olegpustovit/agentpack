//go:build windows

package registry

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func publishExclusive(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func renameDurable(source, target string, replace bool) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}

func removeFileDurable(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".removed-")
	if err != nil {
		return err
	}
	marker := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(marker)
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	if err := renameDurable(path, marker, false); err != nil {
		return err
	}
	// A crash can leave the hidden marker, but the original journal path is durably gone.
	return os.Remove(marker)
}
