package integration_test

import (
	"context"
	"github.com/olegpustovit/agentpack/internal/network"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompiledCLIPassesStderrDescriptorDirectlyToAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses /dev/stderr")
	}
	project := t.TempDir()
	t.Cleanup(func() {
		_, _ = network.NodeRequest(context.Background(), filepath.Join(project, "_agentpack"), "POST", "/api/node/stop")
	})
	if result := runCLI(t, project, "init"); result.err != nil {
		t.Fatalf("init: %s, %v", result.stderr, result.err)
	}
	binary := filepath.Join(project, "agent-fixture")
	// A regular stderr file must stay a file; wrapping it in a copying pipe also
	// prevents an interactive agent from recognizing a terminal descriptor.
	writeFile(t, binary, "#!/bin/sh\n[ -f /dev/stderr ] || exit 19\n")
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(project, "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	command := exec.Command(agentpackBinary, "codex")
	command.Dir = project
	command.Env = installerEnvironment("HOME="+filepath.Join(project, "_home"), "USERPROFILE="+filepath.Join(project, "_home"), "AGENTPACK_HOME="+filepath.Join(project, "_agentpack"), "AGENTPACK_STAGING_ROOT="+filepath.Join(project, "_staging"), "CODEX_PATH="+binary)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		t.Fatalf("agent lost its stderr descriptor: %v; %s", err, readFile(t, stderr.Name()))
	}
}

func TestCompiledCLILaunchersKeepNestedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture; context resolution is tested on Windows")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result := runCLI(t, root, "init"); result.err != nil {
		t.Fatalf("init: %s, %v", result.stderr, result.err)
	}
	fixture := filepath.Join(root, "agent-fixture")
	writeFile(t, fixture, "#!/bin/sh\nprintf 'cwd=%s\\npwd=%s\\n' \"$(pwd -P)\" \"$PWD\"\nprintf '%s\\n' \"$@\"\n")
	if err := os.Chmod(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project1", "project2"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(root, "_agentpack")
	for _, harness := range []struct{ name, env, flag string }{
		{"claude", "CLAUDE_CODE_PATH", ""},
		{"codex", "CODEX_PATH", ""},
		{"opencode", "OPENCODE_PATH", ""},
		{"grok", "GROK_PATH", "--cwd"},
		{"agent", "CURSOR_AGENT_PATH", "--workspace"},
		{"agy", "AGY_PATH", "--add-dir"},
	} {
		t.Run(harness.name, func(t *testing.T) {
			// Repeat one folder for the fast path, then switch to its sibling.
			for _, name := range []string{"project1", "project1", "project2"} {
				workspace := filepath.Join(root, name)
				result := runCLIWithEnv(t, workspace, []string{harness.name},
					harness.env+"="+fixture, "AGENTPACK_HOME="+home,
					"AGENTPACK_STAGING_ROOT="+filepath.Join(root, "_staging"),
					"AGENTPACK_SESSION_WORKER=", "AGENTPACK_PUBLIC_URL=")
				if result.err != nil || !strings.Contains(result.stdout, "cwd="+workspace+"\npwd="+workspace+"\n") {
					t.Fatalf("%s: stdout=%q stderr=%q err=%v", name, result.stdout, result.stderr, result.err)
				}
				if harness.flag != "" && !strings.Contains(result.stdout, harness.flag+"\n"+workspace+"\n") {
					t.Fatalf("workspace argument: %s", result.stdout)
				}
			}
		})
	}
	recent, err := network.Recent(home)
	if err != nil || len(recent) != 12 {
		t.Fatalf("UI recent folders: %+v, %v", recent, err)
	}
	for _, info := range recent {
		if info.DefinitionRoot != root || (info.Directory != filepath.Join(root, "project1") && info.Directory != filepath.Join(root, "project2")) {
			t.Fatalf("UI recorded the wrong folder: %+v", info)
		}
	}
}
