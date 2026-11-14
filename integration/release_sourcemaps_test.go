package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestReleaseDeclaresUISourceMaps(t *testing.T) {
	data, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Archives []struct {
			ID           string
			Meta         bool
			Formats      []string
			NameTemplate string `yaml:"name_template"`
			Files        []any
		}
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, archive := range config.Archives {
		if archive.ID != "ui-sourcemaps" {
			continue
		}
		found = true
		files := []any{map[string]any{"src": "ui/dist/assets/*.map", "dst": "assets", "strip_parent": true}}
		if !archive.Meta || archive.NameTemplate != "ui-sourcemaps_{{ .Version }}" || !slices.Equal(archive.Formats, []string{"tar.gz"}) || !reflect.DeepEqual(archive.Files, files) {
			t.Fatalf("invalid source map archive %+v", archive)
		}
	}
	if !found {
		t.Fatal("release must include its source map archive")
	}
	data, err = os.ReadFile("../ui/vite.config.ts")
	if err != nil || !strings.Contains(string(data), `sourcemap: "hidden"`) {
		t.Fatal("UI release must build hidden source maps", err)
	}
	for _, name := range []string{"../Makefile", "../.github/workflows/release.yml"} {
		data, err = os.ReadFile(name)
		if err != nil || !strings.Contains(string(data), "TestRelease(BinaryDependencies|UISourceMaps)") {
			t.Fatalf("%s must validate source map artifacts: %v", name, err)
		}
	}
}

func TestReleaseUISourceMaps(t *testing.T) {
	dist := os.Getenv("AGENTPACK_RELEASE_DIST")
	if dist == "" {
		t.Skip("run make release-check for real release artifacts")
	}
	files, err := filepath.Glob(filepath.Join(dist, "ui-sourcemaps_*.tar.gz"))
	if err != nil || len(files) != 1 {
		t.Fatalf("source map archives=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	checksums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil || !strings.Contains(string(checksums), hex.EncodeToString(digest[:])+"  "+filepath.Base(files[0])) {
		t.Fatal("source map archive missing from checksums", err)
	}
	zipped, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer zipped.Close()
	archive := tar.NewReader(zipped)
	main := false
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || !strings.HasPrefix(header.Name, "assets/") || !strings.HasSuffix(header.Name, ".map") {
			t.Fatalf("unexpected source map archive member %s", header.Name)
		}
		var source struct {
			Version        int
			File           string
			SourcesContent []string `json:"sourcesContent"`
			Mappings       string
		}
		if err := json.NewDecoder(archive).Decode(&source); err != nil || source.Version != 3 {
			t.Fatalf("invalid source map %s: %v", header.Name, err)
		}
		if strings.HasPrefix(header.Name, "assets/index-") && strings.HasSuffix(header.Name, ".js.map") {
			if source.File != strings.TrimSuffix(filepath.Base(header.Name), ".map") || len(source.SourcesContent) == 0 || source.Mappings == "" {
				t.Fatalf("entry map cannot resolve source locations: %s", header.Name)
			}
			main = true
		}
	}
	if !main {
		t.Fatal("source map archive has no entry map")
	}
}
