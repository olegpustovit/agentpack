package gateway

import (
	"golang.org/x/sys/windows"
)

func listFolderDrives(out *folderListing) error {
	drives, err := windows.GetLogicalDrives()
	if err != nil {
		return err
	}
	for drive := range 26 {
		if drives&(1<<drive) != 0 {
			out.Entries = append(out.Entries, folderEntry{Name: string(rune('A'+drive)) + `:\`})
		}
	}
	return nil
}
