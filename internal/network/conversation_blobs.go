package network

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxBlobBytes        = 20 << 20
	maxSessionBlobBytes = 100 << 20
	maxSessionBlobFiles = 128
)

var errSessionBlobQuota = errors.New("session image storage limit reached")

func validateBlobSHA(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) != 64 {
		return "", errors.New("invalid sha")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", errors.New("invalid sha")
	}
	return sha, nil
}

func blobPath(sessionDir, sha string) string {
	return filepath.Join(sessionDir, "blobs", sha)
}

func sniffBlobMIME(data []byte) string {
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
	sha, err := validateBlobSHA(shaParam)
	if err != nil || sessionDir == "" {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(blobPath(sessionDir, sha))
	if err != nil {
		http.Error(w, "blob not found", http.StatusNotFound)
		return
	}
	ctype := sniffBlobMIME(data)
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

func proxySessionBlob(w http.ResponseWriter, response *http.Response) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBlobBytes+1))
	if err != nil || len(data) > maxBlobBytes {
		http.Error(w, "blob response unavailable", http.StatusBadGateway)
		return
	}
	for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Security-Policy", "X-Content-Type-Options"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

func blobQuotaExceeded(usedBytes int64, files int, incomingBytes int64) bool {
	return files >= maxSessionBlobFiles || usedBytes+incomingBytes > maxSessionBlobBytes
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
		return len(entries), 0, errSessionBlobQuota
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

func storeSessionBlob(sessionDir string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxBlobBytes {
		return "", errors.New("invalid blob size")
	}
	if sniffBlobMIME(data) == "" {
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
	path := blobPath(sessionDir, sha)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("invalid blob file")
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
		return "", errSessionBlobQuota
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return sha, nil
}
