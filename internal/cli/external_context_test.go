package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
)

func TestExternalCommandsPreserveSelectorAndMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	definition, workspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(definition, "external", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "external", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	runner := NewRunner()
	runner.Stdout, runner.Stderr = &out, &errs
	invoke := func(args ...string) (int, error) {
		out.Reset()
		errs.Reset()
		return runner.Run(context.Background(), append([]string{"--definition-root", definition, "--workspace", workspace}, args...))
	}
	if code, err := invoke("mode", "create", "review"); code != 0 || err != nil {
		t.Fatalf("mode: %d %v", code, err)
	}
	if code, err := invoke("--mode", "review", "preflight", "--json"); code != 0 || err != nil {
		t.Fatalf("preflight: %d %v", code, err)
	}
	var report environment.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "review" {
		t.Fatalf("mode=%s", report.Mode)
	}
	if len(report.Findings) > 0 {
		finding := report.Findings[0]
		if code, err := invoke("--mode", "review", "preflight"); code != 0 || err != nil {
			t.Fatalf("human preflight: %d %v", code, err)
		}
		human := strings.Split(out.String(), "\n")
		want := fmt.Sprintf("%s\t%s\t%s", finding.Severity, finding.Code, finding.Message)
		found := false
		for _, line := range human {
			if strings.HasPrefix(line, want) {
				found = true
				if finding.Remediation != nil && finding.Remediation.Message != "" && !strings.Contains(line, "remedy="+finding.Remediation.Message) {
					t.Fatalf("human remediation differs from JSON finding: %q vs %#v", line, finding.Remediation)
				}
				break
			}
		}
		if !found {
			t.Fatalf("human output omitted JSON finding %q", want)
		}
	}
	if code, err := invoke("--mode", "missing", "preflight", "--json"); code == 0 || err == nil {
		t.Fatal("missing mode accepted")
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("failed preflight is not JSON: %v", err)
	}
	if report.OverallStatus != "error" || report.OK {
		t.Fatal("failed preflight reports success")
	}
	if code, err := invoke("--mode", "review", "env", "restore"); code != 0 || err != nil {
		t.Fatalf("restore: %d %v %s", code, err, errs.String())
	}
	bundle := filepath.Join(t.TempDir(), "external.tar.gz")
	if code, err := invoke("env", "export", bundle); code != 0 || err != nil {
		t.Fatalf("export: %d %v", code, err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("checkout mutated: %v", entries)
	}
}

func TestPreflightExitCodesSeparateInvalidInputsFromOperationalFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())

	run := func(t *testing.T, setup func(string)) (int, error) {
		t.Helper()
		definition, workspace := t.TempDir(), t.TempDir()
		if err := manifest.WriteStub(definition, "exit-code", "0.1.0"); err != nil {
			t.Fatal(err)
		}
		setup(definition)
		var out, errs bytes.Buffer
		runner := NewRunner()
		runner.Stdout, runner.Stderr = &out, &errs
		return runner.Run(context.Background(), []string{"--definition-root", definition, "--workspace", workspace, "preflight", "--json"})
	}

	t.Run("malformed contract", func(t *testing.T) {
		code, err := run(t, func(definition string) {
			if err := lockfile.Init(definition, "exit-code", "0.1.0"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(definition, "contract.json"), []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		if code != 2 {
			t.Fatalf("malformed contract exit=%d, want 2: %v", code, err)
		}
	})
	t.Run("malformed lock", func(t *testing.T) {
		code, err := run(t, func(definition string) {
			if err := os.WriteFile(filepath.Join(definition, "pack.lock"), []byte("not = [valid"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		if code != 2 {
			t.Fatalf("malformed lock exit=%d, want 2: %v", code, err)
		}
	})
	t.Run("malformed mode", func(t *testing.T) {
		code, err := run(t, func(definition string) {
			if err := lockfile.Init(definition, "exit-code", "0.1.0"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(definition, "agentpack.toml"), []byte("name = \"invalid\"\nversion = \"0.1.0\"\n[modes.review]\nbase = \"sometimes\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		if code != 2 {
			t.Fatalf("malformed mode exit=%d, want 2: %v", code, err)
		}
	})
	t.Run("operational missing lock", func(t *testing.T) {
		code, err := run(t, func(string) {})
		if code != 5 {
			t.Fatalf("missing lock exit=%d, want 5: %v", code, err)
		}
	})
}

func TestStrictLaunchRefusesUnknownObservationWithoutRunningAgent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Static binary-presence check only; the unknown contract must prevent execution.
	t.Setenv("CLAUDE_CODE_PATH", executable)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	definition, workspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(definition, "strict", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(definition, "strict", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	contract := `{"schema_version":1,"unknown_required":"fail","requirements":[{"id":"observe","target":"claude","selector":{"category":"skill","name":"ambient"},"predicate":"absent","minimum_evidence":"observed"}]}`
	if err := os.WriteFile(filepath.Join(definition, "contract.json"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	runner := NewRunner()
	runner.Stdout, runner.Stderr = &out, &errs
	code, err := runner.Run(context.Background(), []string{"--definition-root", definition, "--workspace", workspace, "--strict-external", "claude"})
	if code == 0 || err == nil {
		t.Fatalf("strict launch passed: %d %v", code, err)
	}
	if code != 3 && code != 4 {
		t.Fatalf("unexpected code %d: %v", code, err)
	}
}
