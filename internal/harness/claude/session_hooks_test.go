package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionStateHooksJoinExistingSettingsHooks(t *testing.T) {
	settings := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "user-stop"}}}}}}
	addSessionStateHooks(settings, "/opt/my ap/ap", "session-x")
	body, _ := json.Marshal(settings)
	text := string(body)
	for _, want := range []string{`user-stop`, `'/opt/my ap/ap' hook-exec session-state --target claude --session session-x --state idle`, `"UserPromptSubmit"`, `"matcher":"permission_prompt"`, `--state needs_you`, `"PostToolUse"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings lack %s: %s", want, text)
		}
	}
	if stops := settings["hooks"].(map[string]any)["Stop"].([]any); len(stops) != 2 {
		t.Fatalf("Stop hooks %d, want the user's and agentpack's", len(stops))
	}
}
