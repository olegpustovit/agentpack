package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/obs"
)

func TestCLIRecordsOneFailureButNoSuccessfulCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	runner := NewRunner()
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	if code, err := runner.Execute(context.Background(), []string{"--version"}); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	if _, err := os.Stat(filepath.Join(home, "logs")); !os.IsNotExist(err) {
		t.Fatal("successful CLI wrote diagnostics", err)
	}
	const header = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	code, err := runner.Execute(obs.WithTrace(context.Background(), header), []string{"unknown-command"})
	if err == nil || code == 0 {
		t.Fatal("fixture command succeeded")
	}
	files, err := filepath.Glob(filepath.Join(home, "logs", "spool", "*.ndjson"))
	if err != nil || len(files) != 1 {
		t.Fatalf("CLI spools=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 1 {
		t.Fatal("failure recorded more than once")
	}
	var event obs.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "cli" || event.Status != code || event.Level != "error" || event.TraceID != obs.ParseTraceParent(header).ID {
		t.Fatalf("CLI event %+v", event)
	}
}

func TestCLIFailureRedactsCredentialsAndHarnessPrompts(t *testing.T) {
	for _, args := range [][]string{{"add", "--token", "private-token", "https://private-user:private-password@example.test/repo"}, {"codex", "exec", "private-prompt", "--prompt=private-flag-prompt"}} {
		home := t.TempDir()
		t.Setenv("AGENTPACK_HOME", home)
		runner := NewRunner()
		runner.Stdout, runner.Stderr = io.Discard, io.Discard
		code := 1
		runner.recordFailure(context.Background(), runner.rootCommand(args, &code), args, 1, errors.New("operation failed"))
		files, err := filepath.Glob(filepath.Join(home, "logs", "spool", "*.ndjson"))
		if err != nil || len(files) != 1 {
			t.Fatal(files, err)
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-token", "private-user", "private-password", "private-prompt", "private-flag-prompt"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("CLI diagnostics leaked", secret)
			}
		}
	}
}
