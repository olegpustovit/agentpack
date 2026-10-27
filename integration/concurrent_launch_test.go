package integration_test

import (
	"bytes"
	"context"
	"github.com/olegpustovit/agentpack/internal/network"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompiledCLILaunchesUpdatedPackWhileAnotherSessionRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture; generation and publication tests also run on Windows")
	}
	project := t.TempDir()
	t.Cleanup(func() {
		_, _ = network.NodeRequest(context.Background(), filepath.Join(project, "_agentpack"), "POST", "/api/node/stop")
	})
	skill := filepath.Join(project, "local-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	before := "---\nname: local-skill\ndescription: fixture\n---\n\n# Before\n"
	after := strings.Replace(before, "Before", "After", 1)
	writeFile(t, skill, before)
	if result := runCLI(t, project, "add", "local-skill"); result.err != nil {
		t.Fatalf("add: %s, %v", result.stderr, result.err)
	}
	started, release := filepath.Join(project, "started"), filepath.Join(project, "release")
	claude := filepath.Join(project, "claude-fixture")
	writeFile(t, claude, "#!/bin/sh\ntouch \"$TEST_STARTED\"\nwhile [ ! -f \"$TEST_RELEASE\" ]; do sleep 0.02; done\n")
	codex := filepath.Join(project, "codex-fixture")
	writeFile(t, codex, "#!/bin/sh\ncat \"$CODEX_HOME/skills/local-skill/SKILL.md\"\n")
	for _, binary := range []string{claude, codex} {
		if err := os.Chmod(binary, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, agentpackBinary, "claude")
	command.Dir = project
	command.Env = installerEnvironment("HOME="+filepath.Join(project, "_home"), "USERPROFILE="+filepath.Join(project, "_home"), "AGENTPACK_HOME="+filepath.Join(project, "_agentpack"), "AGENTPACK_STAGING_ROOT="+filepath.Join(project, "_staging"), "CLAUDE_CODE_PATH="+claude, "TEST_STARTED="+started, "TEST_RELEASE="+release)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	defer func() {
		writeFile(t, release, "exit")
		if err := <-finished; err != nil {
			t.Errorf("original session: %v, %s", err, output.String())
		}
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("original harness did not launch")
		case <-time.After(10 * time.Millisecond):
		}
	}
	originalRoot := stagedModeRoot(t, project)
	originalSkill := filepath.Join(originalRoot, "plugins", "agentpack-bundle", "skills", "local-skill", "SKILL.md")
	writeFile(t, skill, after)
	if result := runCLI(t, project, "lock"); result.err != nil {
		t.Fatalf("lock while running: %s, %v", result.stderr, result.err)
	}
	result := runCLIWithEnv(t, project, []string{"codex"}, "CODEX_PATH="+codex)
	if result.err != nil || !strings.Contains(result.stdout, "# After") {
		t.Fatalf("second launch: stdout=%q stderr=%q err=%v", result.stdout, result.stderr, result.err)
	}
	if stagedModeRoot(t, project) == originalRoot || readFile(t, originalSkill) != before {
		t.Fatal("new launch changed the running session's files")
	}
	select {
	case err := <-finished:
		finished <- err
		t.Fatal("original session exited during the new launch")
	default:
	}
}
