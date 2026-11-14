package integration

import (
	"debug/elf"
	"debug/macho"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/pelletier/go-toml/v2"
)

func validateStateBuilds(contents []byte) error {
	var config struct {
		Builds []struct {
			Env     []string
			LDFlags []string
		}
	}
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return err
	}
	if len(config.Builds) == 0 {
		return fmt.Errorf("missing product builds")
	}
	for _, build := range config.Builds {
		cgo, cc, timestamp := false, false, false
		for _, entry := range build.Env {
			if entry == "SOURCE_DATE_EPOCH=0" {
				timestamp = true
			}
			if entry == "CGO_ENABLED=0" {
				return fmt.Errorf("cgo disabled")
			}
			if entry == "CGO_ENABLED=1" {
				cgo = true
			}
			if strings.HasPrefix(entry, "CC=") && len(entry) > 3 {
				cc = true
			}
		}
		if !cgo || !cc {
			return fmt.Errorf("build requires cgo and a target compiler")
		}
		if !timestamp {
			return fmt.Errorf("deterministic Windows link flags missing")
		}
		if !strings.Contains(strings.Join(build.LDFlags, " "), "-extldflags=-static") {
			return fmt.Errorf("Linux static link flags missing")
		}
	}
	return nil
}

func TestReleaseStateBuildContract(t *testing.T) {
	contents, err := os.ReadFile("../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		content []byte
		good    bool
	}{
		{"configured", contents, true},
		{"disabled", []byte(strings.Replace(string(contents), "CGO_ENABLED=1", "CGO_ENABLED=0", 1)), false},
		{"compiler-missing", []byte(strings.Replace(string(contents), "CC=", "IGNORED_CC=", 1)), false},
		{"dynamic-linux", []byte(strings.Replace(string(contents), "-extldflags=-static", "", 1)), false},
		{"variable-windows-timestamp", []byte(strings.Replace(string(contents), "SOURCE_DATE_EPOCH=0", "IGNORED_SOURCE_DATE_EPOCH=0", 1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateStateBuilds(tc.content); (err == nil) != tc.good {
				t.Fatalf("validation result %v, want valid=%v", err, tc.good)
			}
		})
	}
}

func TestReleaseStateLibraryChecksums(t *testing.T) {
	contents, err := os.ReadFile("../third_party/crsqlite.lock")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		SHA256 map[string]string `toml:"sha256"`
	}
	if err := toml.Unmarshal(contents, &pin); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
		hash := pin.SHA256[target]
		if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
			t.Errorf("missing or invalid library checksum for %s", target)
		}
	}
}

func TestReleaseBinaryDependencies(t *testing.T) {
	dist := os.Getenv("AGENTPACK_RELEASE_DIST")
	if dist == "" {
		t.Skip("set AGENTPACK_RELEASE_DIST to inspect a built release")
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
		t.Run(target, func(t *testing.T) {
			binary := "ap"
			if strings.HasPrefix(target, "windows_") {
				binary += ".exe"
			}
			matches, err := filepath.Glob(filepath.Join(dist, "agentpack_"+target+"*", binary))
			if err != nil || len(matches) != 1 {
				t.Fatalf("missing unique binary for %s: %v %v", target, matches, err)
			}
			var libraries []string
			switch {
			case strings.HasPrefix(target, "linux_"):
				file, err := elf.Open(matches[0])
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				for _, program := range file.Progs {
					if program.Type == elf.PT_INTERP {
						t.Fatal("Linux binary has an interpreter")
					}
				}
				libraries, err = file.ImportedLibraries()
				if err != nil {
					t.Fatal(err)
				}
				if len(libraries) > 0 {
					t.Fatalf("Linux binary has dynamic dependencies: %v", libraries)
				}
			case strings.HasPrefix(target, "darwin_"):
				file, err := macho.Open(matches[0])
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				libraries, err = file.ImportedLibraries()
				if err != nil {
					t.Fatal(err)
				}
			default:
				// debug/pe.ImportedLibraries is unimplemented; LLVM/GNU inspect
				// the complete import table, including ordinal-only libraries.
				args := []string{"-p", matches[0]}
				tool := "objdump"
				if runtime.GOOS == "darwin" {
					tool = "xcrun"
					args = append([]string{"llvm-objdump"}, args...)
				}
				output, err := exec.Command(tool, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("inspect Windows imports: %v: %s", err, output)
				}
				for line := range strings.SplitSeq(string(output), "\n") {
					fields := strings.Fields(line)
					if len(fields) == 3 && fields[0] == "DLL" && fields[1] == "Name:" {
						libraries = append(libraries, fields[2])
					}
				}
				if len(libraries) == 0 {
					t.Fatal("Windows import inspection returned no libraries")
				}

			}
			for _, library := range libraries {
				if strings.Contains(strings.ToLower(library), "sqlite") {
					t.Errorf("shared SQLite dependency: %s", library)
				}
			}
		})
	}
}

func TestReleaseChecksEveryPlatform(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct{ Goos, Goarch string }
				}
			}
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"crsqlite-build", "smoke"} {
		targets := map[string]int{}
		for _, target := range workflow.Jobs[job].Strategy.Matrix.Include {
			targets[target.Goos+"_"+target.Goarch]++
		}
		for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"} {
			if targets[target] != 1 {
				t.Errorf("%s must cover %s exactly once", job, target)
			}
		}
	}
}

func TestReleaseSmokeInputsKeepTheCheckoutClean(t *testing.T) {
	contents, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string
			Steps []struct {
				Uses string
				With struct{ Path string }
			}
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	publish := workflow.Jobs["publish"]
	const prefix = "${{ github.workspace }}/"
	value := publish.Env["AGENTPACK_SMOKED_DIR"]
	if !strings.HasPrefix(value, prefix) {
		t.Fatalf("unrecognised smoke input directory: %s", value)
	}
	path := strings.TrimPrefix(value, prefix)
	if output, err := exec.Command("git", "-C", "..", "check-ignore", "--no-index", path+"/probe").CombinedOutput(); err != nil {
		t.Fatalf("publishing inputs must be ignored to preserve the tested VCS build metadata: %v %s", err, output)
	}
	for _, step := range publish.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With.Path == path {
			return
		}
	}
	t.Fatal("publishing must download the smoke inputs to its ignored verification directory")
}
