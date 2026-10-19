package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompiledCLIPassesStderrDescriptorDirectlyToAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses /dev/stderr")
	}
	project := t.TempDir()
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
