package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olegpustovit/agentpack/internal/gateway"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func TestProjectContentsOmitsMCPCommandsAndSecrets(t *testing.T) {
	locked := lockfile.PackLock{
		MCPServers: []lockfile.MCPServer{{
			Name:      "filesystem",
			Source:    "manifest",
			Launcher:  "npm",
			Status:    "pinned",
			Package:   "@modelcontextprotocol/server-filesystem",
			Version:   "1.2.3",
			Command:   "npx",
			Requested: "@modelcontextprotocol/server-filesystem --token=s3cret",
		}},
	}
	data, err := json.Marshal(projectContents(locked, nil))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, leaked := range []string{`"npx"`, "s3cret", `"command"`, `"requested"`, `"args"`, `"env"`, `"headers"`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("project contents leaked %s: %s", leaked, text)
		}
	}
	if !strings.Contains(text, `"package":"@modelcontextprotocol/server-filesystem"`) {
		t.Fatalf("package name must stay visible: %s", text)
	}
}

func TestProjectModesReportWhatEachModeTurnsOff(t *testing.T) {
	locked := lockfile.PackLock{
		Packages:   []lockfile.Package{{Module: "github.com/a/heavy"}, {Module: "github.com/a/light"}, {Module: "github.com/a/mixed"}},
		MCPServers: []lockfile.MCPServer{{Name: "playwright"}, {Name: "docs"}},
	}
	project := &manifest.Manifest{Modes: map[string]mode.Definition{
		"design": {Base: mode.BaseAll, Disable: []string{"package:github.com/a/heavy", "package-path:github.com/a/mixed:commands/x.md", "mcp:playwright"}},
		"lean":   {Base: mode.BaseNone, Enable: []string{"package:github.com/a/light", "mcp:docs"}},
	}}
	modes := projectContents(locked, project).Modes
	got := map[string]network.ProjectMode{}
	for _, m := range modes {
		got[m.Name] = m
	}
	if len(modes) != 3 || len(got["default"].OffPackages)+len(got["default"].OffMCP) != 0 {
		t.Fatalf("want default, design, lean with default turning nothing off: %+v", modes)
	}
	if d := got["design"]; !slices.Equal(d.OffPackages, []string{"github.com/a/heavy"}) || !slices.Equal(d.PartialPackages, []string{"github.com/a/mixed"}) || !slices.Equal(d.OffMCP, []string{"playwright"}) {
		t.Fatalf("design: %+v", d)
	}
	if l := got["lean"]; !slices.Equal(l.OffPackages, []string{"github.com/a/heavy", "github.com/a/mixed"}) || !slices.Equal(l.OffMCP, []string{"playwright"}) {
		t.Fatalf("lean: %+v", l)
	}
}

func TestSaveNetworkProjectCommitsTheBatchOrRestoresTheDefinition(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: example\ndescription: Example skill\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const url = "https://example.com/mcp"
	input := gateway.ProjectSettingsInput{
		Directory: root,
		Add:       []string{source},
		Servers:   []registry.MCPServerInput{{Name: "example", URL: url}},
		Logins:    map[string]string{url: "login"},
	}
	runner := NewRunner()
	invalid := input
	invalid.Servers = append(slices.Clone(input.Servers), registry.MCPServerInput{Name: "invalid"})
	if err := runner.saveNetworkProject(t.Context(), invalid); err == nil {
		t.Fatal("invalid MCP server was accepted")
	}
	for _, path := range []string{paths.ManifestPath(root), paths.LockPath(root)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed settings save left %s: %v", path, err)
		}
	}
	if err := runner.saveNetworkProject(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(root)
	if err != nil || project == nil || len(project.Dependencies) != 1 || len(project.MCP.Servers) != 1 {
		t.Fatalf("saved definition: %+v %v", project, err)
	}
	locked, err := lockfile.LoadFromPath(paths.LockPath(root))
	if err != nil || len(locked.Packages) != 1 || len(locked.MCPServers) != 1 {
		t.Fatalf("saved lock: %+v %v", locked, err)
	}
	logins, err := mcp.ReadProjectLogins(root)
	if err != nil || logins.URLs[url] != "login" {
		t.Fatalf("saved logins: %+v %v", logins, err)
	}
}

func TestSaveNetworkProjectChecksThePluginMCPSource(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "linear-plugin")
	if err := os.MkdirAll(filepath.Join(source, ".codex-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		".codex-plugin/plugin.json": `{"name":"linear-plugin"}`,
		".mcp.json":                 `{"mcpServers":{"linear":{"url":"https://mcp.linear.app/mcp"}}}`,
	} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner()
	login := mcp.PluginLogin{Module: "github.com/acme/unrelated", URL: "https://mcp.linear.app/mcp", Credential: "trustmind"}
	input := gateway.ProjectSettingsInput{Directory: root, Add: []string{source}, PluginLogins: map[string]mcp.PluginLogin{"linear": login}}
	if err := runner.saveNetworkProject(t.Context(), input); !errors.Is(err, gateway.ErrProjectPluginServerChanged) {
		t.Fatalf("wrong module: %v", err)
	}
	if _, err := os.Stat(paths.ManifestPath(root)); !os.IsNotExist(err) {
		t.Fatalf("failed approval left changes in the manifest: %v", err)
	}
	login.Module = "linear-plugin"
	input.PluginLogins["linear"] = login
	if err := runner.saveNetworkProject(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	logins, err := mcp.ReadProjectLogins(root)
	if err != nil || logins.Plugins["linear"] != login {
		t.Fatalf("plugin login: %+v %v", logins.Plugins, err)
	}
}

func TestSaveNetworkProjectLetsTheWorkerRestage(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	if err := manifest.WriteStub(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.Init(root, "test", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	const id = "session-reload"
	var reloaded atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/state" {
			network.WriteJSON(w, session.TerminalState{Running: true})
			return
		}
		if r.URL.Path != "/restart" || r.URL.Query().Get("when") != "idle" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, release, err := packSync.LockDefinition(ctx, root)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		defer release()
		reloaded.Store(true)
		network.WriteJSON(w, map[string]bool{"deferred": false})
	}))
	defer worker.Close()
	dir := filepath.Join(home, "network", "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := network.PublishWorkerEndpoint(dir, network.WorkerRecord{ID: id, Secret: strings.Repeat("a", 64), Address: strings.TrimPrefix(worker.URL, "http://")}); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	err = session.UpsertSession(t.Context(), store.DB, session.SessionInfo{ID: id, Directory: root, DefinitionRoot: root, Harness: "codex", Mode: "default", Transport: "acp"})
	if err := errors.Join(err, store.Close()); err != nil {
		t.Fatal(err)
	}
	if err := network.RecordLaunchEnvironment(home, id, root, root, "default", harness.Codex); err != nil {
		t.Fatal(err)
	}
	input := gateway.ProjectSettingsInput{Directory: root, Servers: []registry.MCPServerInput{{Name: "example", URL: "https://example.com/mcp"}}}
	if err := NewRunner().saveNetworkProject(t.Context(), input); err != nil {
		t.Fatalf("settings save must let the worker lock the definition during reload: %v", err)
	}
	if !reloaded.Load() {
		t.Fatal("the running chat did not reload")
	}
}
