package network

import (
	"encoding/hex"
	"errors"
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

// SessionEnvironment records only the effective content hash, never configuration or credentials.
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
	decoded, err := hex.DecodeString(string(data))
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid session environment fingerprint")
	}
	return string(data), nil
}

func RecordSessionEnvironment(home, id, digest string) error {
	path, err := environmentPath(home, id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", []byte(digest), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
