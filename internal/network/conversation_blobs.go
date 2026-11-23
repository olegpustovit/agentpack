package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

const (
	MaxBlobBytes        = 20 << 20
	MaxSessionBlobBytes = 100 << 20
	maxSessionBlobFiles = 128
)

var ErrSessionBlobQuota = errors.New("session image storage limit reached")
var ErrBlobIntegrity = errors.New("blob integrity mismatch")

func ValidateBlobSHA(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) != 64 {
		return "", errors.New("invalid sha")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", errors.New("invalid sha")
	}
	return sha, nil
}

func BlobPath(sessionDir, sha string) string {
	return filepath.Join(sessionDir, "blobs", sha)
}

func SniffBlobMIME(data []byte) string {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

func serveSessionBlob(w http.ResponseWriter, _ *http.Request, sessionDir, shaParam string) {
	sha, err := ValidateBlobSHA(shaParam)
	if err != nil || sessionDir == "" {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	data, err := ReadSessionBlob(sessionDir, sha)
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	WriteSessionBlob(w, data)
}

func WriteSessionBlob(w http.ResponseWriter, data []byte) {
	ctype := SniffBlobMIME(data)
	if ctype == "" {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func ReadSessionBlob(sessionDir, sha string) ([]byte, error) {
	root, err := os.OpenRoot(sessionDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Join("blobs", sha)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBlobBytes {
		return nil, ErrBlobIntegrity
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxBlobBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if len(data) > MaxBlobBytes || hex.EncodeToString(sum[:]) != sha {
		return nil, ErrBlobIntegrity
	}
	return data, nil
}

func blobQuotaExceeded(usedBytes int64, files int, incomingBytes int64) bool {
	return files >= maxSessionBlobFiles || usedBytes+incomingBytes > MaxSessionBlobBytes
}

func sessionBlobUsage(dir string) (int, int64, error) {
	directory, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer directory.Close()
	entries, err := directory.Readdir(maxSessionBlobFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, 0, err
	}
	if len(entries) > maxSessionBlobFiles {
		return len(entries), 0, ErrSessionBlobQuota
	}
	var used int64
	for _, entry := range entries {
		if !entry.Mode().IsRegular() {
			return 0, 0, errors.New("invalid blob store entry")
		}
		used += entry.Size()
	}
	return len(entries), used, nil
}

func StoreSessionBlob(sessionDir string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxBlobBytes {
		return "", errors.New("invalid blob size")
	}
	if SniffBlobMIME(data) == "" {
		return "", errors.New("unsupported blob type")
	}
	if sessionDir == "" {
		return "", errors.New("blob store unavailable")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	dir := filepath.Join(sessionDir, "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock := flock.New(filepath.Join(sessionDir, "blobs.lock"))
	if err := lock.Lock(); err != nil {
		return "", err
	}
	defer lock.Unlock()
	path := BlobPath(sessionDir, sha)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("invalid blob file")
		}
		if _, err := ReadSessionBlob(sessionDir, sha); err != nil {
			return "", err
		}
		return sha, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	files, used, err := sessionBlobUsage(dir)
	if err != nil {
		return "", err
	}
	if blobQuotaExceeded(used, files, int64(len(data))) {
		return "", ErrSessionBlobQuota
	}
	file, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return "", err
	}
	defer func() { file.Close(); os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := paths.RenameDurable(file.Name(), path, false); err != nil {
		return "", err
	}
	return sha, nil
}

func AdvertiseBlob(ctx context.Context, store *state.Store, id, sha string, size int64) error {
	if store == nil {
		return nil
	}
	if !strings.HasPrefix(id, "session-") || !ValidPeerSessionID(id) {
		return errors.New("invalid blob session")
	}
	return store.Archive().AdvertiseBlob(ctx, id, sha, size)
}

func ValidPeerSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\")
}
