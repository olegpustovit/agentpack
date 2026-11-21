package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// RacyTreeMetadataWindow keeps a recent edit from becoming a reusable verification stamp.
const RacyTreeMetadataWindow = 3 * time.Second

// TreeMetadata digests the path, type, permission bits, size and modification
// time of every file below root, and reports the newest modification time. It
// reads no contents, so it detects visible changes without proving any bytes.
func TreeMetadata(root string) (string, time.Time, error) {
	hasher := sha256.New()
	var newest time.Time
	// WalkDir visits in lexical order, so the digest is stable.
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		fmt.Fprintf(hasher, "%s\x00%s\x00%d\x00%d\x00", filepath.ToSlash(relative), info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("walk tree %s: %w", root, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), newest, nil
}
