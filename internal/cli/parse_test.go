package cli

import (
	"reflect"
	"testing"
)

func TestParseGlobalOptionsAcrossCommandAndProtectTrailingArguments(t *testing.T) {
	got, err := Parse([]string{"--project-root=/tmp/project", "claude", "--mode", "review", "--", "--debug", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "claude" || got.Global.ProjectRoot != "/tmp/project" || got.Global.Mode != "review" {
		t.Fatalf("unexpected invocation: %#v", got)
	}
	if !reflect.DeepEqual(got.Args, []string{"--", "--debug", "hello"}) {
		t.Fatalf("trailing args = %#v", got.Args)
	}
}

func TestParseDefinitionWorkspaceAndStrictExternal(t *testing.T) {
	got, err := Parse([]string{"--definition-root", "/def", "--workspace=/ws", "--strict-external", "preflight", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "preflight" || got.Global.DefinitionRoot != "/def" || got.Global.WorkspaceRoot != "/ws" || !got.Global.StrictExternal {
		t.Fatalf("unexpected invocation: %#v", got)
	}
	if !reflect.DeepEqual(got.Args, []string{"--json"}) {
		t.Fatalf("args = %#v", got.Args)
	}
}

func TestParseEnvSelector(t *testing.T) {
	got, err := Parse([]string{"--env", "team-review", "--workspace", "/ws", "preflight"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Global.Env != "team-review" || got.Global.WorkspaceRoot != "/ws" || got.Command != "preflight" {
		t.Fatalf("unexpected invocation: %#v", got)
	}
}

func TestMCPAddEnvAssignmentsAndEnvironmentSelector(t *testing.T) {
	invocation, err := parseLeaf([]string{"mcp", "add", "docs", "--command", "npx", "--env", "TOKEN=value", "--env", "team"})
	if err != nil {
		t.Fatal(err)
	}
	server, err := parseMCPAdd(invocation)
	if err != nil || !reflect.DeepEqual(server.Env, map[string]string{"TOKEN": "value"}) {
		t.Fatalf("env = %#v err=%v", server.Env, err)
	}
}

func TestParseLeavesHarnessDebugAndYolo(t *testing.T) {
	got, err := Parse([]string{"--debug", "claude", "--debug", "x", "--yolo"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Global.Debug || got.Global.Yolo || !reflect.DeepEqual(got.Args, []string{"--debug", "x", "--yolo"}) {
		t.Fatalf("unexpected invocation: %#v", got)
	}
}

func TestParseCursorAgentAliasAndMissingValue(t *testing.T) {
	got, err := Parse([]string{"cursor-agent", "--print", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "agent" || !reflect.DeepEqual(got.Args, []string{"--print", "hi"}) {
		t.Fatalf("unexpected invocation: %#v", got)
	}
	if _, err := Parse([]string{"claude", "--mode"}); err == nil {
		t.Fatal("expected missing global value error")
	}
}
