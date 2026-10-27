//go:build !linux && !darwin && !windows

package network

func FolderIdentity(string) (string, error) { return "", ErrUnsupportedTerminal }
