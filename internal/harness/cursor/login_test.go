package cursor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCursorCredentialFiles(t *testing.T) {
	home, config, appData := t.TempDir(), t.TempDir(), t.TempDir()
	for _, tt := range []struct{ goos, want string }{
		{"darwin", filepath.Join(home, ".cursor", "auth.json")},
		{"linux", filepath.Join(config, "cursor", "auth.json")},
		{"windows", filepath.Join(appData, "Cursor", "auth.json")},
	} {
		if got := credentialFile(tt.goos, home, config, appData); got != tt.want {
			t.Errorf("%s: credential path = %q, want %q", tt.goos, got, tt.want)
		}
	}
}

func TestLateSharedCursorLoginReachesStagedMacHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS uses a file symlink")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_CLI_CREDENTIAL_STORE", "")
	fake := t.TempDir()
	if err := os.Mkdir(filepath.Join(fake, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if linked, err := useMacFileLogin(fake); linked || err != nil {
		t.Fatalf("missing login: %v, %v", linked, err)
	}
	source := CredentialFile()
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, data := range []string{`{"accessToken":"header.eyJleHAiOjF9.signature"}`, `{"accessToken":"fixture"}`, `{"accessToken":"replacement"}`} {
		if err := os.WriteFile(source+".new", []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(source+".new", source); err != nil {
			t.Fatal(err)
		}
		if linked, err := useMacFileLogin(fake); linked != (i > 0) || err != nil {
			t.Fatalf("login %d: %v, %v", i, linked, err)
		}
		if i > 0 {
			got, err := os.ReadFile(filepath.Join(fake, ".cursor", "auth.json"))
			if err != nil || string(got) != data {
				t.Fatalf("staged credential mismatch: %v", err)
			}
		}
	}
}

func TestCursorCredentialFreshness(t *testing.T) {
	// The payload is {"exp":1}.
	expired := []byte(`{"accessToken":"header.eyJleHAiOjF9.signature"}`)
	if got := Freshness(expired); !got.Equal(time.Unix(1, 0)) || ValidCredential(expired) {
		t.Fatalf("expired token: freshness %v", got)
	}
	for _, data := range []string{`{"accessToken":"opaque"}`, `{"apiKey":"k"}`} {
		if got := Freshness([]byte(data)); !got.IsZero() || !ValidCredential([]byte(data)) {
			t.Fatalf("%s: freshness %v", data, got)
		}
	}
}
