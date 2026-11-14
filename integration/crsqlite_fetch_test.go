package integration_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCRSQLiteFetchVerifiesBeforeInstalling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fetch script runs in Bash")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3.11+ required")
	}
	_, source, _, _ := runtime.Caller(0)
	script, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(source)), "scripts/fetch-crsqlite.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name             string
		kind             byte
		member, checksum string
		good             bool
	}{
		{"verified-and-cached", tar.TypeReg, "libcrsqlite.a", "valid", true},
		{"missing-checksum", tar.TypeReg, "libcrsqlite.a", "", false},
		{"wrong-checksum", tar.TypeReg, "libcrsqlite.a", strings.Repeat("0", 64), false},
		{"symlink", tar.TypeSymlink, "libcrsqlite.a", "valid", false},
		{"traversal", tar.TypeReg, "../libcrsqlite.a", "valid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "third_party", "tools", ".build/crsqlite/" + commit + "/linux_amd64"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			fixture := filepath.Join(root, "fixture.tar.gz")
			f, err := os.Create(fixture)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			payload := []byte("static library fixture")
			h := &tar.Header{Name: tc.member, Mode: 0644, Size: int64(len(payload)), Typeflag: tc.kind}
			if tc.kind == tar.TypeSymlink {
				h.Size = 0
				h.Linkname = "/etc/passwd"
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				if _, err := tw.Write(payload); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			checksum := tc.checksum
			if checksum == "valid" {
				checksum = fmt.Sprintf("%x", sha256.Sum256(content))
			}
			lock := fmt.Sprintf("commit = %q\n[sha256]\nlinux_amd64 = %q\n", commit, checksum)
			installed := filepath.Join(root, ".build/crsqlite", commit, "linux_amd64/libcrsqlite.a")
			for path, data := range map[string][]byte{
				filepath.Join(root, "scripts/fetch-crsqlite.sh"): script,
				filepath.Join(root, "third_party/crsqlite.lock"): []byte(lock),
				filepath.Join(root, "tools/curl"):                []byte("#!/bin/sh\n[ ! -f \"$FAKE_CURL_MARKER\" ] || exit 99\ntouch \"$FAKE_CURL_MARKER\"\nwhile [ \"$1\" != -o ]; do shift; done\ncp \"$FAKE_ASSET\" \"$2\"\n"),
				installed:                                        []byte("previous library"),
			} {
				if err := os.WriteFile(path, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			run := func() ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts/fetch-crsqlite.sh"), "linux", "amd64")
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_ASSET="+fixture, "FAKE_CURL_MARKER="+filepath.Join(root, "downloaded"))
				return cmd.CombinedOutput()
			}
			out, err := run()
			if (err == nil) != tc.good {
				t.Fatalf("unexpected fetch result: %v\n%s", err, out)
			}
			actual, err := os.ReadFile(installed)
			if err != nil {
				t.Fatal(err)
			}
			if tc.good {
				if string(actual) != string(payload) {
					t.Fatalf("wrong installed bytes: %q", actual)
				}
				if out, err := run(); err != nil {
					t.Fatalf("verified cache was not reused: %v\n%s", err, out)
				}
				if target, err := os.Readlink(filepath.Join(root, ".build/crsqlite/lib")); err != nil || target != commit {
					t.Fatalf("bad pin link %q: %v", target, err)
				}
			} else if string(actual) != "previous library" {
				t.Fatal("failed verification replaced the existing library")
			}
			if checksum == "" {
				if _, err := os.Stat(filepath.Join(root, "downloaded")); !os.IsNotExist(err) {
					t.Fatal("missing checksum reached the network")
				}
			}
		})
	}
}

func TestPublishingRequiresTheSmokeTestedBinary(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python required")
	}
	_, source, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(filepath.Dir(source)), "scripts/verify-smoked-binary.py")
	dir := t.TempDir()
	archive, err := os.Create(filepath.Join(dir, "agentpack_1.0.0_linux_amd64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ap", Mode: 0755, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "ap")
	for _, payload := range []string{"ok", "changed"} {
		if err := os.WriteFile(binary, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		cmd := exec.CommandContext(ctx, "python3", script, "linux", "amd64", binary)
		cmd.Env = append(os.Environ(), "AGENTPACK_SMOKED_DIR="+dir)
		output, err := cmd.CombinedOutput()
		cancel()
		if (err == nil) != (payload == "ok") {
			t.Fatalf("payload=%q err=%v output=%s", payload, err, output)
		}
	}
}
