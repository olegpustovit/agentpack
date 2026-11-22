//go:build !windows

package network

func listFolderDrives(*folderListing) error { return errFolderPath }
