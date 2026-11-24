package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/registry"
)

func TestParseMCPAdd(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, args []string)
	}{
		{name: "neither", args: []string{}, wantErr: "exactly one of --command or --url"},
		{name: "both", args: []string{"--command", "x", "--url", "https://e.com"}, wantErr: "exactly one of --command or --url"},
		{name: "type with command", args: []string{"--command", "x", "--type", "sse"}, wantErr: "--type applies to --url"},
		{name: "header with command", args: []string{"--command", "x", "--header", "A=b"}, wantErr: "--header applies to --url"},
		{name: "env with url", args: []string{"--url", "https://e.com", "--env", "A=b"}, wantErr: "apply to --command"},
		{name: "bad header", args: []string{"--url", "https://e.com", "--header", "nokey"}, wantErr: "KEY=VALUE"},
		{name: "empty url", args: []string{"--url", ""}, wantErr: "non-empty"},
		{name: "url defaults to http", args: []string{"--url", "https://e.com/mcp"}, check: func(t *testing.T, args []string) {
			server, err := parseMCPAdd(parsed(t, args))
			if err != nil || server.Command != nil || *server.URL != "https://e.com/mcp" || *server.Type != "http" || len(server.Headers) != 0 {
				t.Fatalf("server=%+v err=%v", server, err)
			}
		}},
		{name: "url with type and repeated headers", args: []string{"--url=https://e.com", "--type", "sse", "--header", "A=1", "--header", "B=x=y", "--header", "C=3"}, check: func(t *testing.T, args []string) {
			server, err := parseMCPAdd(parsed(t, args))
			if err != nil || *server.Type != "sse" || len(server.Headers) != 3 || server.Headers["B"] != "x=y" {
				t.Fatalf("server=%+v err=%v", server, err)
			}
		}},
		{name: "unflagged argument", args: []string{"--command", "npx", "pkg"}, wantErr: "after --"},
		{name: "command still works", args: []string{"--command", "npx", "--env", "K=v", "--allow-unpinned", "--", "-y", "pkg"}, check: func(t *testing.T, args []string) {
			server, err := parseMCPAdd(parsed(t, args))
			if err != nil || !boolFlag(parsed(t, args), "allow-unpinned") || server.Args[0] != "-y" || *server.Command != "npx" || len(server.Args) != 2 || server.Env["K"] != "v" || server.URL != nil || server.Type != nil {
				t.Fatalf("server=%+v err=%v", server, err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.wantErr != "" {
				if _, err := parseMCPAdd(parsed(t, test.args)); err == nil || !strings.Contains(err.Error(), test.wantErr) {
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
	if code, err := runner.Execute(context.Background(), []string{"--project-root", root, "init"}); err != nil || code != 0 {
		t.Fatalf("init code=%d err=%v", code, err)
	}
	code, err := runner.Execute(context.Background(), []string{"--project-root", root, "mcp", "add", "rf", "--url", "https://researcher.microapps.space/mcp", "--header", "X-Key=v"})
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

func TestGatewayMCPInstallMatchesMCPAdd(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	runner := NewRunner()
	runner.Stdout, runner.Stderr = io.Discard, io.Discard
	if code, err := runner.Execute(context.Background(), []string{"--project-root", root, "init"}); err != nil || code != 0 {
		t.Fatalf("init code=%d err=%v", code, err)
	}
	for _, bad := range []registry.MCPServerInput{{Name: "x"}, {Name: "x", Command: "npx", URL: "https://e.com"}, {Name: "x", Command: "npx", Type: "sse"}, {Name: "x", URL: "https://e.com", Args: []string{"a"}}} {
		if err := runner.installNetworkMCP(context.Background(), root, bad, false); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := runner.installNetworkMCP(context.Background(), root, registry.MCPServerInput{Name: "rf", URL: "https://e.com/mcp"}, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "agentpack.toml"))
	lock, _ := os.ReadFile(filepath.Join(root, "pack.lock"))
	if !strings.Contains(string(raw), `type = "http"`) || !strings.Contains(string(raw), `url = "https://e.com/mcp"`) || !strings.Contains(string(lock), "launcher = 'remote'") {
		t.Fatalf("agentpack.toml:\n%s\npack.lock:\n%s", raw, lock)
	}
}

// parsed parses `mcp add docs ARGS...` through the real command tree.
func parsed(t *testing.T, args []string) Invocation {
	t.Helper()
	invocation, err := parseLeaf(append([]string{"mcp", "add", "docs"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

// parseLeaf resolves argv to its leaf command and parses its flags as Execute would.
func parseLeaf(argv []string) (Invocation, error) {
	command, rest, err := NewRunner().rootCommand(argv, new(int)).Find(argv)
	if err != nil {
		return Invocation{}, err
	}
	if err := command.ParseFlags(rest); err != nil {
		return Invocation{}, err
	}
	return leafInvocation(command, Global{}, command.Flags().Args()), nil
}

// A server named for `mcp login` is looked up in the project's pack.lock and manifest.
func TestMCPLoginTargetReadsTheProjectLock(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	manifestText := "name = \"demo\"\nversion = \"0.0.1\"\n\n[dependencies]\n\n[mcp.servers.docs]\nurl = \"https://docs.example/mcp\"\ntype = \"http\"\n"
	if err := os.WriteFile(filepath.Join(project, "agentpack.toml"), []byte(manifestText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "pack.lock"), []byte("lockfile_version = 2\n\n[meta]\nname = \"demo\"\nversion = \"0.0.1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := Runner{}.mcpLoginTarget(Invocation{Global: Global{ProjectRoot: project}}, "docs")
	if err != nil || server.URL == nil || *server.URL != "https://docs.example/mcp" {
		t.Fatalf("mcpLoginTarget = %+v, %v", server, err)
	}
}
