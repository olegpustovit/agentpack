package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOMPRealRuntimeWithPackHooksMCPAndLSP(t *testing.T) {
	binary := os.Getenv("AGENTPACK_TEST_OMP")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OMP to the released OMP executable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal(err)
	}
	home, workspace, source := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, kind := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		t.Setenv("XDG_"+kind+"_HOME", filepath.Join(home, kind))
	}
	t.Setenv("PI_CONFIG_FILES", "")
	t.Setenv("OMP_AUTH_BROKER_URL", "")
	t.Setenv("OMP_AUTH_BROKER_TOKEN", "")
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("OMP_PATH", binary)
	t.Setenv("PI_CONFIG_DIR", ".omp")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "default-profile-not-selected"))
	t.Setenv("OMP_PROFILE", "work")
	t.Setenv("OMP_SMOKE_LOG", filepath.Join(home, "events.jsonl"))
	t.Setenv("OMP_FIXTURE_KEY", "local-only")
	var mutex sync.Mutex
	var requests []string
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages json.RawMessage   `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		requests = append(requests, string(request.Messages))
		if len(request.Tools) > 0 {
			calls++
		}

		name, arguments := "", ""
		switch {
		case len(request.Tools) == 0:
		case calls == 1:
			name, arguments = "bash", `{"command":"printf forbidden"}`
		case calls == 2:
			name, arguments = "bash", `{"command":"printf original"}`
		case calls == 3:
			name, arguments = "bash", `{"command":"printf timeout"}`
		case calls == 4:
			name, arguments = "write", `{"path":"xd://mcp__agentpack_bundle_fixture_ping","content":"{}"}`
		case calls == 5:
			name, arguments = "write", `{"path":"xd://lsp","content":"{\"action\":\"diagnostics\",\"file\":\"fixture.go\"}"}`
		}
		delta := map[string]any{"role": "assistant", "content": "OMP_DONE"}
		finish := "stop"
		if name != "" {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", calls), "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("fixture-%d", calls), "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		chunk, _ = json.Marshal(map[string]any{"id": fmt.Sprintf("fixture-%d", calls), "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	write := func(root, name, value string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	native := filepath.Join(home, ".omp", "profiles", "work", "agent")
	config := "enabledProviders: [fixture]\nmodelRoles:\n  default: fixture/fixture\n  smol: fixture/fixture\n  slow: fixture/fixture\n  title: fixture/fixture\n"
	models := fmt.Sprintf("providers:\n  fixture:\n    baseUrl: %s/v1\n    apiKey: OMP_FIXTURE_KEY\n    api: openai-completions\n    models:\n      - id: fixture\n        name: Fixture\n        reasoning: false\n        input: [text]\n        cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0}\n        contextWindow: 128000\n        maxTokens: 1000\n", server.URL)
	write(native, "config.yml", config)
	write(native, "models.yml", models)
	write(native, "skills/native-sentinel/SKILL.md", "---\nname: native-sentinel\ndescription: Native profile skill\n---\nNative skill.\n")
	write(workspace, "fixture.go", "package fixture\n")
	script, err := os.ReadFile("testdata/omp-runtime.js")
	if err != nil {
		t.Fatal(err)
	}
	write(source, "scripts/runtime.js", string(script))
	write(source, ".omp-plugin/plugin.json", `{"name":"runtime-fixture"}`)
	write(source, "skills/pack-sentinel/SKILL.md", "---\nname: pack-sentinel\ndescription: Pack sentinel skill\n---\nPack skill.\n")
	write(source, "rules/always.mdc", "---\nalwaysApply: true\n---\nOMP_RULE_CONTEXT\n")
	var hooks map[string]any = make(map[string]any)
	for event, mode := range map[string]string{"SessionStart": "start", "UserPromptSubmit": "input", "PreToolUse": "pre", "PostToolUse": "post"} {
		matcher := ""
		if event == "PreToolUse" || event == "PostToolUse" {
			matcher = "Bash"
		}
		hooks[event] = []any{map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": bun, "args": []string{"${PLUGIN_ROOT}/scripts/runtime.js", mode}, "timeout": 1}}}}
	}
	hooksData, _ := json.Marshal(map[string]any{"hooks": hooks})
	write(source, "hooks/hooks.json", string(hooksData))
	mcpData, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"command": bun, "args": []string{"${PLUGIN_ROOT}/scripts/runtime.js", "mcp"}, "cwd": workspace}}})
	write(source, "mcp.json", string(mcpData))
	lspData, _ := json.Marshal(map[string]any{"fixture": map[string]any{"command": bun, "args": []string{"${PLUGIN_ROOT}/scripts/runtime.js", "lsp"}, "extensionToLanguage": map[string]string{".go": "go"}}})
	write(source, ".lsp.json", string(lspData))
	if result := runCLI(t, workspace, "init"); result.err != nil {
		t.Fatalf("init: %v %s", result.err, result.stderr)
	}
	if result := runCLI(t, workspace, "add", source); result.err != nil {
		t.Fatalf("add: %v %s", result.err, result.stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, agentpackBinary, "--no-progress", "--yolo", "omp", "--", "--print", "--model", "fixture/fixture", "--no-title", "--no-pty", "OMP_SMOKE_RUN")
	command.Dir = workspace
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real OMP launch: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OMP_DONE") {
		t.Fatalf("native final response missing: %s", output)
	}
	events, err := os.ReadFile(os.Getenv("OMP_SMOKE_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"mode":"start"`, `"mode":"input"`, `"mode":"pre"`, `"mode":"post"`, `"method":"tools/call"`, `"mode":"lsp","method":"initialize"`, "OMP_REVISED"} {
		if !strings.Contains(string(events), marker) {
			t.Errorf("missing native event %s:\n%s\noutput:\n%s", marker, events, output)
		}
	}
	mutex.Lock()
	captured := strings.Join(requests, "\n")
	mutex.Unlock()
	for _, marker := range []string{"pack-sentinel", "native-sentinel", "OMP_RULE_CONTEXT", "OMP_START_CONTEXT", "OMP_INPUT_CONTEXT", "OMP_BLOCKED", "OMP_REDACTED", "hook-exec exited", "OMP_MCP_OK"} {
		if !strings.Contains(captured, marker) {
			t.Errorf("native model context missing %s", marker)
		}
	}
	for name, expected := range map[string]string{"config.yml": config, "models.yml": models} {
		data, err := os.ReadFile(filepath.Join(native, name))
		if err != nil || string(data) != expected {
			t.Errorf("native %s changed: %v", name, err)
		}
	}
	var sessions []string
	if err := filepath.WalkDir(filepath.Join(native, "sessions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
			sessions = append(sessions, path)
		}
		return nil
	}); err != nil || len(sessions) != 1 {
		t.Fatalf("native profile session was not retained: %v %v", sessions, err)
	}
	if result := runCLI(t, workspace, "sync"); result.err != nil {
		t.Fatalf("rebuild: %v %s", result.err, result.stderr)
	}
	resume := exec.CommandContext(ctx, agentpackBinary, "--no-progress", "--yolo", "omp", "--", "--print", "--continue", "--model", "fixture/fixture", "--no-title", "--no-pty", "OMP_RESUME_RUN")
	resume.Dir = workspace
	if output, err := resume.CombinedOutput(); err != nil {
		t.Fatalf("native resume after rebuild: %v\n%s", err, output)
	}
	session, err := os.ReadFile(sessions[0])
	if err != nil || !strings.Contains(string(session), "OMP_SMOKE_RUN") || !strings.Contains(string(session), "OMP_RESUME_RUN") {
		t.Fatalf("resume did not preserve native session: %v", err)
	}
	mutex.Lock()
	beforeDenied := calls
	mutex.Unlock()
	denied := exec.CommandContext(ctx, agentpackBinary, "--no-progress", "--yolo", "omp", "--", "--print", "--model", "fixture/fixture", "--no-session", "--no-title", "--no-pty", "OMP_DENY_INPUT")
	denied.Dir = workspace
	deniedOutput, deniedErr := denied.CombinedOutput()
	if deniedErr == nil {
		t.Errorf("headless input denial returned success: %s", deniedOutput)
	}
	mutex.Lock()
	afterDenied := calls
	mutex.Unlock()
	if afterDenied != beforeDenied {
		t.Errorf("denied headless prompt reached provider: %d -> %d", beforeDenied, afterDenied)
	}

	if result := runCLI(t, workspace, "sync", "--verify-only"); result.err != nil {
		t.Fatalf("native runtime changed managed staging: %v %s", result.err, result.stderr)
	}
}
