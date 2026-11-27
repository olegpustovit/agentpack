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

func TestSaveNetworkProjectRemovesOnlyDirectDependencies(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: example\ndescription: Example skill\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner()
	if err := runner.installNetworkPackage(t.Context(), root, source, false, ""); err != nil {
		t.Fatal(err)
	}
	module := filepath.Base(source)
	input := gateway.ProjectSettingsInput{Directory: root, RemoveModule: "github.com/example/" + module}
	if err := runner.saveNetworkProject(t.Context(), input); !errors.Is(err, gateway.ErrPackageNotDirect) {
		t.Fatalf("remove non-direct module: %v", err)
	}
	input.RemoveModule = module
	if err := runner.saveNetworkProject(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(root)
	if err != nil || project == nil || len(project.Dependencies) != 0 {
		t.Fatalf("manifest after removal: %+v %v", project, err)
	}
	locked, err := lockfile.LoadFromPath(paths.LockPath(root))
	if err != nil || len(locked.Packages) != 0 {
		t.Fatalf("lock after removal: %+v %v", locked, err)
	}
}

func TestSaveNetworkProjectRenamesMCP(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	root := t.TempDir()
	const original = `mcp = { servers = { docs = { url = "https://example.com/mcp", type = "http" }, shell = { command = "bunx", args = ["tool"] } }, enabled = { docs = false } } # preserve this setting

[modes.lean]
base = "none"
enable = ["mcp:docs", "mcp:shell"]

[modes.quiet]
disable = ["mcp:docs"] # preserve this selector
`
	if err := os.WriteFile(paths.ManifestPath(root), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "mcp.json"), []byte(`{"external":{"command":"example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := manifest.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	pin := mcp.Describe("shell", mcp.Entry{Source: mcp.Manifest, Server: project.MCP.Servers["shell"]})
	pin.Status, pin.Version, pin.Integrity = lockfile.MCPPinned, "1.2.3", "sha512-example"
	lock := lockfile.EmptyForProject(root)
	lock.MCPServers = []lockfile.MCPServer{pin}
	if err := lock.Save(root); err != nil {
		t.Fatal(err)
	}
	const url = "https://example.com/mcp"
	loginPath, err := paths.ProjectMCPLoginsPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.SaveProjectLogin(loginPath, url, "work"); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner()
	for _, rejected := range []struct{ old, name, message string }{
		{"docs", "bad name", "must contain only"},
		{"docs", "SHELL", "already in use"},
		{"external", "owned", "project manifest"},
	} {
		err := runner.saveNetworkProject(t.Context(), gateway.ProjectSettingsInput{
			Directory: root, RenameMCP: map[string]string{rejected.old: rejected.name},
		})
		var rejection gateway.ProjectMCPRenameError
		if !errors.As(err, &rejection) || !strings.Contains(rejection.Error(), rejected.message) {
			t.Fatalf("renaming %q to %q: %v", rejected.old, rejected.name, err)
		}
	}
	unchanged, err := os.ReadFile(paths.ManifestPath(root))
	if err != nil || string(unchanged) != original {
		t.Fatalf("rejected rename changed manifest: %s %v", unchanged, err)
	}

	if err := runner.saveNetworkProject(t.Context(), gateway.ProjectSettingsInput{
		Directory: root, RenameMCP: map[string]string{"docs": "handbook", "shell": "tools"},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ManifestPath(root))
	want := strings.NewReplacer("docs", "handbook", "shell", "tools").Replace(original)
	if err != nil || string(data) != want {
		t.Fatalf("renamed manifest: %s %v", data, err)
	}
	logins, err := mcp.ReadProjectLogins(root)
	if err != nil || logins.URLs[url] != "work" {
		t.Fatalf("renamed server lost the URL's login: %+v %v", logins, err)
	}
	locked, err := runner.Service.LoadLock(root)
	if err != nil || len(locked.MCPServers) != 1 || locked.MCPServers[0].Name != "tools" || locked.MCPServers[0].Version != pin.Version {
		t.Fatalf("rename lost the command server's lock pin: %+v %v", locked.MCPServers, err)
	}

	const nextURL = "https://example.org/mcp"
	if err := runner.saveNetworkProject(t.Context(), gateway.ProjectSettingsInput{
		Directory: root, RenameMCP: map[string]string{"handbook": "manual"},
		Servers: []registry.MCPServerInput{{Name: "manual", URL: nextURL, Type: "http"}},
	}); err != nil {
		t.Fatal(err)
	}
	project, err = manifest.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	server := project.MCP.Servers["manual"]
	enabled, hasOverride := project.MCP.Enabled["manual"]
	if server.URL == nil || *server.URL != nextURL || !hasOverride || enabled {
		t.Fatalf("combined rename and URL update: %+v, enabled settings %+v", server, project.MCP.Enabled)
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
	var rejection gateway.ProjectMCPRenameError
	if err := runner.saveNetworkProject(t.Context(), gateway.ProjectSettingsInput{
		Directory: root, RenameMCP: map[string]string{"linear": "work"},
	}); !errors.As(err, &rejection) || !strings.Contains(rejection.Error(), "project manifest") {
		t.Fatalf("renaming a plugin server: %v", err)
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
	endpoint := "https://example.com/mcp"
	disabled := true
	if err := manifest.AddMCPServer(root, "example", mcp.Server{URL: &endpoint, Disabled: &disabled}); err != nil {
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
	environmentFile := filepath.Join(dir, "environment")
	data, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	var launch network.Environment
	if err := json.Unmarshal(data, &launch); err != nil {
		t.Fatal(err)
	}
	launch.Binary = "previous binary"
	data, err = json.Marshal(launch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	input := gateway.ProjectSettingsInput{Directory: root, MCPEnabled: map[string]bool{"example": true}}
	if err := NewRunner().saveNetworkProject(t.Context(), input); err != nil {
		t.Fatalf("settings save must let the worker lock the definition during reload: %v", err)
	}
	if !reloaded.Load() {
		t.Fatal("the running chat did not reload")
	}
}
