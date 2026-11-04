package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func TestAdaptAgentLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantMethod string
		pass       bool
	}{
		{
			name:       "xai session update",
			line:       `{"jsonrpc":"2.0","method":"x.ai/session/update","params":{"sessionId":"s1","update":{"toolCall":{"toolCallId":"t1"}}}}`,
			wantMethod: "session/update",
		},
		{
			name:       "underscore prefix",
			line:       `{"jsonrpc":"2.0","method":"_x.ai/session/update","params":{"sessionId":"s1","update":{"agentMessageChunk":{"content":{"type":"text","text":{"text":"hi"}}}}}}`,
			wantMethod: "session/update",
		},
		{
			name: "authenticate passthrough",
			line: `{"jsonrpc":"2.0","method":"_x.ai/authenticate","params":{"methodId":"grok.com"}}`,
			pass: true,
		},
		{
			name: "billing dropped",
			line: `{"jsonrpc":"2.0","method":"_x.ai/billing","params":{}}`,
			pass: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []byte(tt.line)
			got := adaptAgentLine(in)
			if tt.pass {
				if got.Forward != nil {
					t.Fatalf("forward=%q", got.Forward)
				}
				return
			}
			if tt.wantMethod == "" {
				if got.Forward == nil || len(got.Forward) != 0 {
					t.Fatalf("forward=%q", got.Forward)
				}
				return
			}
			if len(got.Forward) == 0 {
				t.Fatal("expected rewritten line")
			}
			var out struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(got.Forward, &out); err != nil {
				t.Fatal(err)
			}
			if out.Method != tt.wantMethod {
				t.Fatalf("method=%q", out.Method)
			}
		})
	}
}

func TestACPLaunchRequiresStoredCredentials(t *testing.T) {
	home := t.TempDir()
	agentpackHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTPACK_HOME", agentpackHome)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_PATH", executable)
	native := filepath.Join(home, ".grok")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "auth.json"), []byte(`{"token":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := paths.SharedGrokCredentialsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "auth.json.initialized"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	_, err = acpLaunch(base.LaunchContext{
		ProjectRoot: project, WorkspaceRoot: project, Mode: mode.ImplicitEffective(),
	})
	if err == nil || !strings.Contains(err.Error(), "stored login") {
		t.Fatalf("acpLaunch error = %v", err)
	}
}

func TestACPLaunchAuthenticatesWithExistingCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_PATH", executable)
	project := t.TempDir()
	mode := mode.ImplicitEffective()
	shared, err := paths.SharedGrokCredentialsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "auth.json"), []byte(`{"token":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	launch, err := acpLaunch(base.LaunchContext{ProjectRoot: project, WorkspaceRoot: project, Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	if launch.AuthenticateMethodID != "grok.com" {
		t.Fatalf("AuthenticateMethodID = %q", launch.AuthenticateMethodID)
	}
	staged, err := paths.StagingGrokHomeDirForMode(project, mode.Name())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(staged, "auth.json")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("staged Grok credentials: info=%v err=%v", info, err)
	}
}
