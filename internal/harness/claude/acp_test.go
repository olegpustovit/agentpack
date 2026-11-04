package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteSessionPlugins(t *testing.T) {
	rewrite := rewriteSessionPlugins([]string{"/plugins/agentpack"}, "/plugins/agentpack-bundle/agentpack-settings.json")
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp"}}`)
	out := rewrite(line)
	var envelope struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatal(err)
	}
	claudeCode, _ := envelope.Params.Meta["claudeCode"].(map[string]any)
	options, _ := claudeCode["options"].(map[string]any)
	plugins, _ := options["plugins"].([]any)
	if plugin, _ := plugins[0].(map[string]any); len(plugins) != 1 || plugin["type"] != "local" || plugin["path"] != "/plugins/agentpack" {
		t.Fatalf("plugins=%v", plugins)
	}
	if options["settings"] != "/plugins/agentpack-bundle/agentpack-settings.json" {
		t.Fatalf("settings=%v", options["settings"])
	}
	for _, method := range []string{"session/load", "session/resume"} {
		line := []byte(`{"method":"` + method + `","params":{"sessionId":"id","cwd":"/tmp"}}`)
		var resumed struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(rewrite(line), &resumed); err != nil {
			t.Fatal(err)
		}
		meta, _ := resumed.Params.Meta["claudeCode"].(map[string]any)
		options, _ := meta["options"].(map[string]any)
		if options["settings"] != "/plugins/agentpack-bundle/agentpack-settings.json" {
			t.Fatalf("%s settings=%v", method, options["settings"])
		}
	}
	if got := rewrite([]byte(`{"method":"session/prompt"}`)); string(got) != `{"method":"session/prompt"}` {
		t.Fatalf("unexpected rewrite: %s", got)
	}
}
func TestClaudeCodeExecutableOverride(t *testing.T) {
	dir := t.TempDir()
	adapterBinary := filepath.Join(dir, "claude-adapter")
	configuredBinary := filepath.Join(dir, "claude-configured")
	for _, path := range []string{adapterBinary, configuredBinary} {
		if err := os.WriteFile(path, []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CODE_EXECUTABLE", adapterBinary)
	t.Setenv("CLAUDE_CODE_PATH", configuredBinary)
	if got, err := claudeCodeExecutable(); err != nil || got != adapterBinary {
		t.Fatalf("CLAUDE_CODE_EXECUTABLE: got %q, %v", got, err)
	}
	t.Setenv("CLAUDE_CODE_EXECUTABLE", "")
	if got, err := claudeCodeExecutable(); err != nil || got != configuredBinary {
		t.Fatalf("CLAUDE_CODE_PATH fallback: got %q, %v", got, err)
	}
}
