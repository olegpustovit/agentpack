package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

func diagnosticFixture(t *testing.T) (string, obs.Event) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`CREATE TABLE mesh_nodes(node_id TEXT PRIMARY KEY,name TEXT,app_version TEXT);
INSERT INTO mesh_nodes VALUES ('fixture-node','PRIVATE-HOST','v1.2.0');
INSERT INTO sessions(id,name,directory,started_by,owner_email) VALUES ('session-fixture','PRIVATE-SESSION','/home/PRIVATE-USER/project','{"user":"PRIVATE-EMAIL"}','PRIVATE-OWNER@example.com');`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	logs, err := obs.Open(home, obs.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace := obs.NewTrace()
	event := obs.Event{TS: time.Now().Add(-time.Minute), TraceID: trace.ID, SpanID: trace.Span, NodeID: "fixture-node", Proc: "gateway", Kind: "ui.error", Name: "TypeError", Level: "error", SessionID: "session-fixture", User: "PRIVATE-EMAIL", Stack: "TypeError\n at https://PRIVATE-HOST/assets/index-abc123.js:4:5\n /home/PRIVATE-USER/project/file.go:42\n C:\\Users\\PRIVATE-WINDOWS\\source.go:2", Attrs: map[string]any{"version": "v1.2.0", "ui_build_id": "abc123", "error_name": "TypeError", "message": "fixture failed", "route": "/sessions", "Authorization": "Bearer PRIVATE-TOKEN"}}
	event = event.Sanitized()
	logs.Write(event)
	parent := event
	parent.Level, parent.Kind, parent.Name, parent.SpanID = "info", "http", "GET /api/sessions", obs.NewTrace().Span
	parent.TS = parent.TS.Add(-time.Second)
	logs.Write(parent)
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	return home, event
}

func TestLogsCommandsOfflineTextAndJSON(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
			_, event := diagnosticFixture(t)
			for _, command := range []string{"issues", "show", "trace", "query", "resolve", "reopen", "ignore", "bundle"} {
				t.Run(command, func(t *testing.T) {
					args := []string{"logs", command}
					if command == "trace" {
						args = append(args, event.TraceID)
					} else if command != "issues" && command != "query" {
						args = append(args, event.Fingerprint)
					}
					if command == "resolve" {
						args = append(args, "--version", "v1.2.0", "--note", "fixed")
					}
					if command == "query" {
						args = append(args, "--since", "2h", "--kind", "ui.error", "--level", "error", "--session", event.SessionID, "--node", event.NodeID, "--trace", event.TraceID, "--grep", "fixture", "--limit", "1")
					}
					outputPath := filepath.Join(t.TempDir(), "repro")
					if command == "bundle" {
						args = append(args, "-o", outputPath)
					}
					if jsonOutput {
						args = append(args, "--json")
					}
					var output strings.Builder
					runner := Runner{Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard}
					code, err := runner.Execute(context.Background(), args)
					if err != nil || code != 0 || output.Len() == 0 {
						t.Fatalf("code=%d err=%v output=%q", code, err, output.String())
					}
					if jsonOutput && !json.Valid([]byte(output.String())) {
						t.Fatalf("invalid JSON: %s", output.String())
					}
					if command == "show" && !jsonOutput && (!strings.Contains(output.String(), "abc123") || !strings.Contains(output.String(), "assets/index-abc123.js")) {
						t.Fatalf("missing source map identity: %s", output.String())
					}
					if command == "resolve" && !strings.Contains(output.String(), "resolved") {
						t.Fatalf("--version was not the fix version: %s", output.String())
					}
					if command == "bundle" {
						checkDiagnosticBundle(t, outputPath)
					}
				})
			}
		})
	}
}

func checkDiagnosticBundle(t *testing.T, path string) {
	t.Helper()
	for _, name := range []string{"issue.json", "traces.ndjson", "rows.json", "versions.json"} {
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "traces.ndjson" {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 2 || !json.Valid([]byte(lines[0])) || !json.Valid([]byte(lines[1])) {
				t.Fatalf("trace not complete: %s", data)
			}
		} else if !json.Valid(data) {
			t.Fatalf("invalid %s: %s", name, data)
		}
		for _, secret := range []string{"PRIVATE-", "/home/", `C:\\Users`} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("%s leaked %q: %s", name, secret, data)
			}
		}
		if name == "rows.json" && (!strings.Contains(string(data), "session-fixture") || strings.Contains(string(data), "unavailable_tables")) {
			t.Fatalf("involved rows missing: %s", data)
		}
		if name == "versions.json" && !strings.Contains(string(data), "assets/index-abc123.js") {
			t.Fatalf("lost public source map identity: %s", data)
		}
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("private file mode: %v %v", info, err)
		}
	}
}

func TestLogsValidationAndDirectRunner(t *testing.T) {
	home, event := diagnosticFixture(t)
	runner := Runner{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
	if code, err := runner.Run(context.Background(), []string{"logs", "resolve", event.Fingerprint, "--version", "v1.2.0", "--json"}); err != nil || code != 0 {
		t.Fatalf("direct runner: %d %v", code, err)
	}
	for _, args := range [][]string{{"query", "SELECT * FROM events"}, {"query", "--limit", "0"}, {"query", "--since", "yesterday"}, {"query", "--since", "-1h"}, {"resolve", event.Fingerprint}, {"show", "bad"}, {"bundle", event.Fingerprint}, {"--proxy", "issues"}} {
		if code, err := runner.Run(context.Background(), append([]string{"logs"}, args...)); err == nil || code == 0 {
			t.Fatalf("accepted invalid args %v: %d %v", args, code, err)
		}
	}
	output := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(output, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.writeLogBundle(context.Background(), home, event.Fingerprint, output); err == nil {
		t.Fatal("overwrote existing destination")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}
