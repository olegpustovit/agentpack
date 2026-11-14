package state

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"path/filepath"
	"testing"
)

func TestExtensionLoaded(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	var version string
	if err := store.DB.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "3.54.1" {
		t.Fatalf("SQLite version %s differs from the extension source headers", version)
	}
	var siteID []byte
	if err := store.DB.QueryRow("SELECT crsql_site_id()").Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	if len(siteID) != 16 {
		t.Fatalf("site ID has %d bytes, want 16", len(siteID))
	}
}

// The Go cache does not notice changed external C libraries. A source-pin path
// in the cgo flags changes the package/link action when the pin changes.
func TestLinkPathsTrackSourcePin(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(source), "../..")
	lock, err := os.ReadFile(filepath.Join(root, "third_party/crsqlite.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Commit string `toml:"commit"`
	}
	if err := toml.Unmarshal(lock, &pin); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(source), "crsqlite.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
		platform := strings.Split(target, "_")
		directive := fmt.Sprintf("#cgo %s,%s LDFLAGS: -L${SRCDIR}/../../.build/crsqlite/%s/%s -lcrsqlite", platform[0], platform[1], pin.Commit, target)
		if !strings.Contains(string(contents), directive) {
			t.Errorf("cgo link path must track source pin for %s; update crsqlite.go with the lock", target)
		}
	}
}
