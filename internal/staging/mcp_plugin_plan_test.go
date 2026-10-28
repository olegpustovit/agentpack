package staging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

func TestMCPPluginPlanCapturesBytesAndRejectsChangedSource(t *testing.T) {
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	key := strings.Repeat("a", 64)
	source, err := cache.EntryDir(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(source, "scripts", "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ready\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	command := "node"
	entry := mcp.Entry{Module: "github.com/acme/plugin", CacheKey: key, Source: mcp.Plugin, Server: mcp.Server{Command: &command, Args: []string{"${CLAUDE_PLUGIN_ROOT}/scripts/run.sh"}}}
	effective := mode.ImplicitEffective()
	plan, err := PlanMCPPluginRoots(t.TempDir(), effective.Name(), effective, mcp.Entries{"run": entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || string(plan.Files[0].Contents) != "#!/bin/sh\necho ready\n" || plan.Files[0].Mode != sourceInfo.Mode() {
		t.Fatalf("plan did not capture exact plugin file: %#v", plan.Files)
	}
	if args := plan.Entries["run"].Server.Args; len(args) != 1 || filepath.Clean(args[0]) != plan.Files[0].Path {
		t.Fatalf("plugin root was not substituted: %#v", plan.Entries["run"].Server.Args)
	}
	if _, err := os.Stat(plan.Files[0].Path); !os.IsNotExist(err) {
		t.Fatalf("planning wrote plugin output: %v", err)
	}
	if err := os.WriteFile(script, []byte("changed after plan"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("changed source passed validation: %v", err)
	}
}
