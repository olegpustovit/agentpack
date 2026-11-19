package network

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func environmentPath(home, id string) (string, error) {
	if !strings.HasPrefix(id, "session-") || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(home, "network", "sessions", id, "environment"), nil
}

// BinaryTag identifies the running agentpack executable. Environment digests depend on staging
// code, so a digest is only comparable with one computed by the same binary; dev builds of a
// dirty tree share a commit, so the file's own identity is used.
func BinaryTag() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(executable)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", executable, info.Size(), info.ModTime().UnixNano())
}

// SessionEnvironment records only the effective content hash, never configuration or credentials.
// A digest recorded by another binary reads as absent, so an update never looks like a package change.
func SessionEnvironment(home, id string) (string, error) {
	path, err := environmentPath(home, id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	digest, tag, _ := strings.Cut(string(data), "\n")
	if tag == "" || tag != BinaryTag() {
		return "", nil
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid session environment fingerprint")
	}
	return digest, nil
}

func RecordSessionEnvironment(home, id, digest string) error {
	path, err := environmentPath(home, id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", []byte(digest+"\n"+BinaryTag()), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
