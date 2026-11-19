package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://API.githubcopilot.com/mcp/":    "https://api.githubcopilot.com/mcp",
		"https://api.githubcopilot.com:443/mcp": "https://api.githubcopilot.com/mcp",
		"http://localhost:8080/mcp#x":           "http://localhost:8080/mcp",
		"https://mcp.example.com":               "https://mcp.example.com",
		"https://mcp.example.com/?tenant=a":     "https://mcp.example.com?tenant=a",
		"http://[::1]:80/mcp":                   "http://[::1]/mcp",
	} {
		got, err := CanonicalURL(raw)
		if err != nil || got != want {
			t.Errorf("CanonicalURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"ftp://x/y", "https://user:pw@x/mcp", "/relative"} {
		if _, err := CanonicalURL(raw); err == nil {
			t.Errorf("CanonicalURL(%q) succeeded", raw)
		}
	}
}

func TestApplyCredentials(t *testing.T) {
	str := func(value string) *string { return &value }
	yes := true
	index := CredentialIndex{Credentials: []CredentialRef{
		{ID: "c-gh", Match: "https://api.githubcopilot.com/mcp", Label: "GitHub", Kind: CredentialOAuth, Plugins: []string{"github.com/acme/approved"}},
		{ID: "c-pat", Match: "https://pat.example.com/mcp", Label: "PAT", Kind: CredentialHeader},
		{ID: "c-env", Label: "gh-env", Kind: CredentialEnv},
	}}
	entries := Entries{
		"github":     {Server: Server{URL: str("https://api.githubcopilot.com/mcp/"), Headers: map[string]string{"X-Toolsets": "repos"}}, Source: Manifest},
		"sse":        {Server: Server{Type: str("sse"), URL: str("https://pat.example.com/mcp")}, Source: DotAgents},
		"own-auth":   {Server: Server{URL: str("https://pat.example.com/mcp"), Headers: map[string]string{"authorization": "Bearer mine"}}, Source: Manifest},
		"unmatched":  {Server: Server{URL: str("https://other.example.com/mcp")}, Source: Manifest},
		"disabled":   {Server: Server{URL: str("https://pat.example.com/mcp"), Disabled: &yes}, Source: Manifest},
		"plugin-no":  {Server: Server{URL: str("https://api.githubcopilot.com/mcp")}, Source: Plugin, Module: "github.com/acme/other"},
		"plugin-yes": {Server: Server{URL: str("https://api.githubcopilot.com/mcp")}, Source: Plugin, Module: "github.com/acme/approved"},
		"stdio-env":  {Server: Server{Command: str("docker"), Args: []string{"run", "ghcr.io/github/github-mcp-server"}, Env: map[string]string{"A": "1"}, Credential: "GH-ENV"}, Source: Manifest},
		"missing":    {Server: Server{URL: str("https://x.example.com/mcp"), Credential: "nope"}, Source: Manifest},
		"wrong-kind": {Server: Server{Command: str("x"), Credential: "GitHub"}, Source: Manifest},
	}
	got, notes := ApplyCredentials(entries, index, "/bin/agentpack")
	bridged := func(name string) []string {
		t.Helper()
		server := got[name].Server
		if server.Command == nil || *server.Command != "/bin/agentpack" {
			return nil
		}
		return server.Args
	}
	want := map[string][]string{
		"github":     {"mcp-bridge", "c-gh", "--url", "https://api.githubcopilot.com/mcp/", "--header", "X-Toolsets=repos"},
		"sse":        {"mcp-bridge", "c-pat", "--url", "https://pat.example.com/mcp", "--sse"},
		"plugin-yes": {"mcp-bridge", "c-gh", "--url", "https://api.githubcopilot.com/mcp"},
		"stdio-env":  {"mcp-bridge", "c-env", "--", "docker", "run", "ghcr.io/github/github-mcp-server"},
	}
	for name := range entries {
		if args := bridged(name); !reflect.DeepEqual(args, want[name]) {
			t.Errorf("%s: args %q, want %q", name, args, want[name])
		}
	}
	if !reflect.DeepEqual(got["stdio-env"].Server.Env, map[string]string{"A": "1"}) || got["github"].Server.URL != nil {
		t.Fatalf("bridged servers kept the wrong fields: %+v %+v", got["stdio-env"].Server, got["github"].Server)
	}
	joined := strings.Join(notes, "\n")
	for _, fragment := range []string{"plugin-no from github.com/acme/other", "approve GitHub github.com/acme/other", `names credential "nope"`, `wrong-kind: credential "GitHub" (oauth) does not apply`} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("notes lack %q:\n%s", fragment, joined)
		}
	}
	if !reflect.DeepEqual(entries["github"].Server.Headers, map[string]string{"X-Toolsets": "repos"}) || entries["github"].Server.Command != nil {
		t.Fatal("ApplyCredentials modified its input")
	}
}

func TestReadCredentialIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if index, err := ReadCredentialIndex(path); err != nil || len(index.Credentials) != 0 {
		t.Fatalf("missing index = %+v, %v", index, err)
	}
	if err := os.WriteFile(path, []byte(`{"credentials":[{"id":"a","label":"A","kind":"header","match":"https://x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if index, err := ReadCredentialIndex(path); err != nil || len(index.Credentials) != 1 || index.Credentials[0].ID != "a" {
		t.Fatalf("index = %+v, %v", index, err)
	}
}

func TestLoginURLOnlyForServersASharedLoginCanServe(t *testing.T) {
	remote := func(raw string) Server { return Server{URL: &raw} }
	withHeader := remote("https://a.example/mcp")
	withHeader.Headers = map[string]string{"authorization": "Bearer x"}
	named := remote("https://a.example/mcp")
	named.Credential = "work"
	command := "npx"
	for _, test := range []struct {
		server Server
		want   string
	}{
		{remote("https://MCP.Example.com:443/mcp/"), "https://mcp.example.com/mcp"},
		{remote("https://a.example/mcp?key=secret"), ""},
		{withHeader, ""},
		{named, ""},
		{Server{Command: &command}, ""},
	} {
		if got := LoginURL(test.server); got != test.want {
			t.Errorf("LoginURL(%+v) = %q, want %q", test.server, got, test.want)
		}
	}
}
