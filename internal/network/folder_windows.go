//go:build windows

package network

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func FolderIdentity(directory string) (string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", directory)
	}
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var identity windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x:%x", identity.VolumeSerialNumber, identity.FileIndexHigh, identity.FileIndexLow), nil
}
