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
		{ID: "c-lin-a", Match: "https://mcp.linear.app/mcp", Label: "Linear A", Kind: CredentialOAuth},
		{ID: "c-lin-b", Match: "https://mcp.linear.app/mcp", Label: "Linear B", Kind: CredentialOAuth},
		{ID: "c-st", Match: "https://stale.example.com/mcp", Label: "Stale first", Kind: CredentialOAuth},
	}}
	logins := ProjectLogins{"https://mcp.linear.app/mcp": "c-lin-b", "https://stale.example.com/mcp": "c-removed"}
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
		"picked":     {Server: Server{URL: str("https://mcp.linear.app/mcp")}, Source: Manifest},
		"stale-pick": {Server: Server{URL: str("https://stale.example.com/mcp")}, Source: Manifest},
		"explicit":   {Server: Server{URL: str("https://mcp.linear.app/mcp"), Credential: "linear a"}, Source: Manifest},
	}
	got, notes := ApplyCredentials(entries, index, logins, "/bin/agentpack")
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
		"picked":     {"mcp-bridge", "c-lin-b", "--url", "https://mcp.linear.app/mcp"},
		"stale-pick": {"mcp-bridge", "c-st", "--url", "https://stale.example.com/mcp"},
		"explicit":   {"mcp-bridge", "c-lin-a", "--url", "https://mcp.linear.app/mcp"},
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
	for _, fragment := range []string{"plugin-no from github.com/acme/other", "approve GitHub github.com/acme/other", `names credential "nope"`, `wrong-kind: credential "GitHub" (oauth) does not apply`, `stale-pick: the login picked for this project is gone; using "Stale first"`} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("notes lack %q:\n%s", fragment, joined)
		}
	}
	if !reflect.DeepEqual(entries["github"].Server.Headers, map[string]string{"X-Toolsets": "repos"}) || entries["github"].Server.Command != nil {
		t.Fatal("ApplyCredentials modified its input")
	}
}

func TestApplyCredentialsWithoutPicksUsesTheFirstLogin(t *testing.T) {
	url := "https://mcp.linear.app/mcp"
	index := CredentialIndex{Credentials: []CredentialRef{{ID: "a", Match: url, Kind: CredentialOAuth}, {ID: "b", Match: url, Kind: CredentialOAuth}}}
	entries := Entries{"linear": {Server: Server{URL: &url}, Source: Manifest}}
	for _, logins := range []ProjectLogins{nil, {"https://other.example/mcp": "b"}} {
		got, notes := ApplyCredentials(entries, index, logins, "/ap")
		if args := got["linear"].Server.Args; len(args) < 2 || args[1] != "a" || len(notes) != 0 {
			t.Fatalf("logins %v: args %q notes %q", logins, args, notes)
		}
	}
}

func TestSaveProjectLoginRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects", "h", "mcp-logins.json")
	if logins, err := ReadProjectLogins(path); err != nil || len(logins) != 0 {
		t.Fatalf("missing file = %v, %v", logins, err)
	}
	if err := SaveProjectLogin(path, "https://MCP.Linear.app/mcp/", "b"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectLogin(path, "https://x.example/mcp", "c"); err != nil {
		t.Fatal(err)
	}
	logins, err := ReadProjectLogins(path)
	if err != nil || !reflect.DeepEqual(logins, ProjectLogins{"https://mcp.linear.app/mcp": "b", "https://x.example/mcp": "c"}) {
		t.Fatalf("logins = %v, %v", logins, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if err := SaveProjectLogin(path, "https://mcp.linear.app/mcp", ""); err != nil {
		t.Fatal(err)
	}
	if logins, _ := ReadProjectLogins(path); !reflect.DeepEqual(logins, ProjectLogins{"https://x.example/mcp": "c"}) {
		t.Fatalf("after delete = %v", logins)
	}
	if err := SaveProjectLogin(path, "https://x.example/mcp/", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty picks left the file: %v", err)
	}
	if err := SaveProjectLogin(path, "ftp://x", "c"); err == nil {
		t.Fatal("saved a non-http URL")
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
