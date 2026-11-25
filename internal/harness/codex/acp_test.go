package codex

import "testing"

func TestWithoutSubagentsKeepsExistingConfig(t *testing.T) {
	got, err := withoutSubagents(`{"model":"gpt-5"}`)
	if err != nil || got != `{"agents.enabled":false,"model":"gpt-5"}` {
		t.Fatalf("got %s, %v", got, err)
	}
	if _, err := withoutSubagents("nope"); err == nil {
		t.Fatal("invalid CODEX_CONFIG accepted")
	}
}
