//go:build !windows

package gateway

func listFolderDrives(*folderListing) error { return errFolderPath }
