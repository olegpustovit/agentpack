package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func New() base.Harness {
	return base.Definition{Target: base.Cursor, Root: stagedRoot, Reset: resetPaths, BeforeReset: preReset, Setup: prepare, ProjectMCP: planMCP, AfterStage: finalize, WorkspaceOverlay: materializeAgentsOverlay, Check: verify, Launch: launch}
}

func launch(ctx base.LaunchContext) (*exec.Cmd, error) {
	arguments := append([]string(nil), ctx.Arguments...)
	if !base.HasFlagValue(arguments, "--workspace") {
		workspace := ctx.WorkspaceRoot
		if workspace == "" {
			workspace = base.WorkspaceRoot(ctx.ProjectRoot)
		}
		arguments = append([]string{"--workspace", workspace}, arguments...)
	}
	if ctx.Yolo {
		arguments = base.PrependOnce(arguments, "--force", "--yolo")
	}
	if allowsTrust(arguments) && !base.HasAny(arguments, "--trust") {
		arguments = append([]string{"--trust"}, arguments...)
	}
	binary, err := base.ResolveBinary("CURSOR_AGENT_PATH", "cursor-agent")
	if err != nil {
		return nil, err
	}
	home, err := paths.StagingCursorHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	command := exec.Command(binary, arguments...)
	environment := append(os.Environ(), "HOME="+home, "CURSOR_CONFIG_DIR="+filepath.Join(home, ".cursor"))
	if runtime.GOOS == "windows" {
		environment = append(environment, "USERPROFILE="+home, "APPDATA="+filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"))
	} else if runtime.GOOS == "linux" {
		environment = append(environment, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"))
	}
	if realHome, err := os.UserHomeDir(); err == nil {
		for _, entry := range []struct{ key, value string }{
			{"CARGO_HOME", filepath.Join(realHome, ".cargo")},
			{"RUSTUP_HOME", filepath.Join(realHome, ".rustup")},
			{"DOCKER_CONFIG", filepath.Join(realHome, ".docker")},
			{"CURSOR_DATA_DIR", filepath.Join(realHome, ".cursor")},
		} {
			if _, exists := os.LookupEnv(entry.key); !exists {
				environment = append(environment, entry.key+"="+entry.value)
			}
		}
	}
	command.Env = environment
	return command, nil
}
func allowsTrust(arguments []string) bool {
	for index, argument := range arguments {
		if argument == "-p" || argument == "--print" || strings.HasPrefix(argument, "--output-format=") || (argument == "--output-format" && index+1 < len(arguments)) {
			return true
		}
	}
	return false
}
func stagedRoot(ctx base.StageContext) (string, error) {
	return paths.StagingCursorPackPluginDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
}
func resetPaths(ctx base.StageContext) ([]string, error) {
	bundle, err := paths.StagingCursorBundleDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	home, err := paths.StagingCursorHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	return []string{bundle, home}, nil
}
func preReset(ctx base.StageContext) error {
	if ctx.StrictExternal {
		return nil
	}
	return cleanupOverlay(ctx.ProjectRoot)
}

func prepare(ctx base.StageContext) error {
	bundle, err := paths.StagingCursorBundleDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		return err
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := base.CopySelectedEntries(filepath.Join(home, ".cursor"), bundle, []string{"cli-config.json", "mcp.json"}); err != nil {
			return err
		}
	}
	if err := writeManifests(bundle); err != nil {
		return err
	}
	pack, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := base.CopySelectedEntries(filepath.Join(home, ".cursor"), pack, []string{"skills"}); err != nil {
			return err
		}
	}
	if !keepAttribution() {
		if err := forceAttribution(bundle); err != nil {
			return err
		}
		if err := forceAttribution(pack); err != nil {
			return err
		}
	}
	return nil
}
func forceAttribution(root string) error {
	path := filepath.Join(root, "cli-config.json")
	value := make(map[string]any)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &value)
	}
	return writeJSON(path, patchAttribution(value))
}
func writeMCP(entries mcp.Entries, ctx base.StageContext) error {
	projection, err := planMCP(entries, ctx)
	if err != nil {
		return err
	}
	return base.WriteMCPProjection(projection)
}
func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	pack, warnings, err := RenderMCP(entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	user := []byte(nil)
	var inputs []base.MCPProjectedInput
	if home, err := os.UserHomeDir(); err == nil {
		seedPath := filepath.Join(home, ".cursor", "mcp.json")
		user, err = base.ReadMCPSeed(seedPath)
		if err != nil {
			return base.MCPProjection{}, err
		}
		input, err := base.MCPInput(seedPath, user)
		if err != nil {
			return base.MCPProjection{}, err
		}
		inputs = append(inputs, input)
	}
	merged, err := MergeMCPFilesData(pack, user)
	if err != nil {
		return base.MCPProjection{}, err
	}
	packDigest, err := base.MCPJSONScopeDigest(pack, "mcpServers")
	if err != nil {
		return base.MCPProjection{}, err
	}
	files := []base.MCPProjectedFile{{Path: filepath.Join(root, "mcp.json"), Contents: pack, Scope: "mcpServers", ScopeDigest: packDigest, MergeOrder: []string{"plugin definitions", "agentpack.toml", ".agents/mcp.json", "Cursor target projection"}}}
	if merged != nil {
		fakeHome, err := paths.StagingCursorHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
		if err != nil {
			return base.MCPProjection{}, err
		}
		digest, err := base.MCPJSONScopeDigest(merged, "mcpServers")
		if err != nil {
			return base.MCPProjection{}, err
		}
		files = append(files, base.MCPProjectedFile{Path: filepath.Join(fakeHome, ".cursor", "mcp.json"), Contents: merged, Scope: "mcpServers", ScopeDigest: digest, MergeOrder: []string{"pack MCP projection", "user ~/.cursor/mcp.json (existing names win)"}})
	}
	servers := base.ProjectedMCPServers(entries)
	for index := range servers {
		if len(user) != 0 {
			if prior, err := readMCPBytes(user); err == nil {
				if _, exists := prior.Servers[servers[index].Name]; exists {
					servers[index].Winner = "user"
				}
			}
		}
	}
	base.MarkMCPWarnings(servers, warnings)
	return base.MCPProjection{Files: files, Servers: servers, Warnings: warnings, Inputs: inputs}, nil
}
func finalize(_ mcp.Entries, ctx base.StageContext) error {
	pack, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if err := writeReadme(pack); err != nil {
		return err
	}
	return materializeFakeHome(ctx)
}
func verify(ctx base.StageContext) error {
	bundle, err := paths.StagingCursorBundleDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return err
	}
	pack, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	home, err := paths.StagingCursorHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(bundle, ".cursor-plugin", "marketplace.json"), filepath.Join(pack, ".cursor-plugin", "plugin.json"), filepath.Join(home, ".cursor")} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("cursor staging missing %s: %w", path, err)
		}
	}
	if ctx.LaunchTarget != nil && *ctx.LaunchTarget == base.Cursor {
		entries, err := readOverlayManifest(ctx.ProjectRoot)
		if err != nil {
			return err
		}
		for _, path := range entries {
			if _, err := os.Lstat(path); err != nil {
				return fmt.Errorf("cursor workspace overlay missing at %s: %w", path, err)
			}
		}
	}
	return nil
}
func keepAttribution() bool {
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
