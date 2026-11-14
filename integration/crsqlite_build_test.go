package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCRSQLiteBuildRejectsInvalidInputsBeforeFetching(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("source builder requires Bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash unavailable")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("source builder requires Python 3.11+")
	}
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Dir(filepath.Dir(source))
	script, err := os.ReadFile(filepath.Join(repo, "scripts", "build-crsqlite.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(repo, "third_party", "crsqlite.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, lock string
		args       []string
		want       string
	}{
		{"usage", string(lock), nil, "usage:"},
		{"target", string(lock), []string{"linux", "riscv64"}, "unsupported cr-sqlite target"},
		{"pin", strings.Replace(string(lock), "8e32195704ccaea8508f7d378e11dc2dc0bb3d2e", "main", 1), []string{"linux", "amd64"}, "invalid third_party/crsqlite.lock"},
		{"toolchain", string(lock), []string{"linux", "amd64"}, "install Zig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "third_party", "tools"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "scripts", "build-crsqlite.sh")
			for name, body := range map[string][]byte{
				path: script,
				filepath.Join(root, "third_party", "crsqlite.lock"): []byte(tc.lock),
				filepath.Join(root, "tools", "zig"):                 []byte("#!/bin/sh\necho wrong-version\n"),
				filepath.Join(root, "tools", "git"):                 []byte("#!/bin/sh\necho UNEXPECTED_NETWORK >&2\nexit 99\n"),
			} {
				if err := os.WriteFile(name, body, 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, append([]string{path}, tc.args...)...)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "UNEXPECTED_NETWORK") {
				t.Fatalf("got %v: %s; want failure %q before fetching", err, out, tc.want)
			}
		})
	}
}
