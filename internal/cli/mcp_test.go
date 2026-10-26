package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMCPAdd(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, args []string)
	}{
		{name: "neither", args: []string{"--no-sync"}, wantErr: "exactly one of --command or --url"},
		{name: "both", args: []string{"--command", "x", "--url", "https://e.com"}, wantErr: "exactly one of --command or --url"},
		{name: "type with command", args: []string{"--command", "x", "--type", "sse"}, wantErr: "--type applies to --url"},
		{name: "header with command", args: []string{"--command", "x", "--header", "A=b"}, wantErr: "--header applies to --url"},
		{name: "env with url", args: []string{"--url", "https://e.com", "--env", "A=b"}, wantErr: "apply to --command"},
		{name: "bad header", args: []string{"--url", "https://e.com", "--header", "nokey"}, wantErr: "KEY=VALUE"},
		{name: "empty url", args: []string{"--url", ""}, wantErr: "non-empty"},
		{name: "url defaults to http", args: []string{"--url", "https://e.com/mcp", "--no-sync"}, check: func(t *testing.T, args []string) {
			server, noSync, _, err := parseMCPAdd(args)
			if err != nil || !noSync || server.Command != nil || *server.URL != "https://e.com/mcp" || *server.Type != "http" || len(server.Headers) != 0 {
				t.Fatalf("server=%+v noSync=%v err=%v", server, noSync, err)
			}
		}},
		{name: "url with type and repeated headers", args: []string{"--url=https://e.com", "--type", "sse", "--header", "A=1", "B=x=y", "--header", "C=3"}, check: func(t *testing.T, args []string) {
			server, _, _, err := parseMCPAdd(args)
			if err != nil || *server.Type != "sse" || len(server.Headers) != 3 || server.Headers["B"] != "x=y" {
				t.Fatalf("server=%+v err=%v", server, err)
			}
		}},
		{name: "command still works", args: []string{"--command", "npx", "--args", "-y", "pkg", "--env", "K=v", "--allow-unpinned"}, check: func(t *testing.T, args []string) {
			server, _, allow, err := parseMCPAdd(args)
			if err != nil || !allow || *server.Command != "npx" || len(server.Args) != 2 || server.Env["K"] != "v" || server.URL != nil || server.Type != nil {
				t.Fatalf("server=%+v err=%v", server, err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.wantErr != "" {
				if _, _, _, err := parseMCPAdd(test.args); err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			test.check(t, test.args)
		})
	}
}

func TestMCPAddRemoteWritesManifest(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	runner := NewRunner()
	if code, err := runner.Run(context.Background(), []string{"--project-root", root, "init"}); err != nil || code != 0 {
		t.Fatalf("init code=%d err=%v", code, err)
	}
	code, err := runner.Run(context.Background(), []string{"--project-root", root, "mcp", "add", "rf", "--url", "https://researcher.microapps.space/mcp", "--header", "X-Key=v", "--no-sync"})
	if err != nil || code != 0 {
		t.Fatalf("mcp add code=%d err=%v", code, err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "agentpack.toml"))
	for _, want := range []string{`type = "http"`, `url = "https://researcher.microapps.space/mcp"`, `headers = { X-Key = "v" }`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("manifest lacks %q:\n%s", want, raw)
		}
	}
	lock, _ := os.ReadFile(filepath.Join(root, "pack.lock"))
	if !strings.Contains(string(lock), "launcher = 'remote'") || !strings.Contains(string(lock), "unpinnable") || strings.Contains(string(lock), "X-Key") {
		t.Fatalf("pack.lock:\n%s", lock)
	}
}
