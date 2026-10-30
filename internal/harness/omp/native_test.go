package omp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/omp"
)

func TestNativeDiscovery(t *testing.T) {
	binary := os.Getenv("AGENTPACK_TEST_OMP")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OMP to a released OMP binary")
	}
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, kind := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		t.Setenv("XDG_"+kind+"_HOME", filepath.Join(home, kind))
	}
	t.Setenv("PI_CONFIG_FILES", "")
	t.Setenv("PI_CONFIG_DIR", ".omp")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".omp", "agent"))
	t.Setenv("OMP_PROFILE", "")
	t.Setenv("PI_PROFILE", "")
	t.Setenv("OPENAI_API_KEY", "diagnostic-no-model-calls")
	t.Setenv("OMP_PATH", binary)
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	stage := base.StageContext{ProjectRoot: workspace}
	if err := omp.New().Prepare(stage); err != nil {
		t.Fatal(err)
	}
	root, err := omp.New().StagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	for path, source := range map[string]string{
		"skills/omp-smoke/SKILL.md": "---\nname: omp-smoke\ndescription: Verify pack skills\n---\nOMP_SKILL_BODY\n",
		"rules/smoke.mdc":           "---\nalwaysApply: true\n---\nOMP_RULE_MARKER\n",
		"commands/smoke.md":         "---\ndescription: Verify pack commands\n---\nOMP_COMMAND_MARKER $ARGUMENTS\n",
		"agents/omp-smoke-agent.md": "---\nname: omp-smoke-agent\ndescription: Verify pack agents\n---\nOMP_AGENT_MARKER\n",
	} {
		parsed, err := artifacts.Parse(path, source, "")
		if err != nil {
			t.Fatal(err)
		}
		rendered := parsed.Render(base.OMP)
		destination := filepath.Join(root, rendered.RelativePath)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, []byte(rendered.Contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	launchCmd, err := omp.New().LaunchCommand(base.LaunchContext{ProjectRoot: workspace, Arguments: []string{"--mode", "rpc", "--no-session", "--no-pty"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, launchCmd.Path, launchCmd.Args[1:]...)
	cmd.Dir = workspace
	var stderr strings.Builder
	cmd.Stderr = &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); cmd.Process.Kill(); cmd.Wait() }()
	if _, err := input.Write([]byte("{\"id\":\"state\",\"type\":\"get_state\"}\n{\"id\":\"commands\",\"type\":\"get_available_commands\"}\n")); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	replies := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		var reply struct {
			ID      string `json:"id"`
			Success bool   `json:"success"`
		}
		if json.Unmarshal([]byte(line), &reply) != nil || (reply.ID != "state" && reply.ID != "commands") {
			continue
		}
		if !reply.Success {
			t.Fatalf("native RPC failed: %s", line)
		}
		replies[reply.ID] = line
		if len(replies) == 2 {
			break
		}
	}
	if len(replies) != 2 {
		t.Fatalf("native startup: replies=%v error=%v stderr=%s", replies, scanner.Err(), stderr.String())
	}
	for _, marker := range []string{"omp-smoke", "omp-smoke-agent", "OMP_RULE_MARKER", "Attribution policy"} {
		if !strings.Contains(replies["state"], marker) {
			t.Errorf("native system prompt missing %s", marker)
		}
	}
	if !strings.Contains(replies["commands"], "smoke") {
		t.Error("native command absent")
	}
}
