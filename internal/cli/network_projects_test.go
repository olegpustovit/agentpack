package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/network"
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
