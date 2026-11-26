package agy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

const workspaceOverlay = ".agents/plugins/agentpack-bundle"

// OverlayMarker is the file that says a workspace overlay is Agentpack's to
// replace; its content is the staged bundle the overlay was copied from.
const OverlayMarker = ".agentpack-overlay"

// OverlayTempPrefix starts the directories under .agents where an overlay is
// built; they are never project inputs.
const OverlayTempPrefix = ".agentpack-bundle-"

const unownedOverlay = "agentpack: " + workspaceOverlay + " is not agentpack's, so Antigravity will not get pack content; move it and launch again"

func New() base.Harness {
	return base.Harness{Target: base.Agy, Artifacts: &artifactLayout, Root: stagedRoot, Reset: resetPaths, Setup: prepare, ProjectMCP: planMCP, WorkspaceOverlay: finalizeWorkspace, Check: verify, Launch: launch, LaunchACP: acpLaunch, Inheritance: base.Inheritance{
		Note:    "Antigravity leaves HOME untouched and writes a copy of the pack as a workspace plugin",
		Paths:   func(string) []string { return base.HomeFiles(".gemini/settings.json", ".gemini/GEMINI.md") },
		Overlay: []string{".agents/plugins/agentpack-bundle"},
	}}
}

func launch(ctx base.LaunchContext) (*exec.Cmd, func() error, error) {
	arguments := append([]string(nil), ctx.Arguments...)
	if !base.HasFlagValue(arguments, "--add-dir") {
		workspace := ctx.WorkspaceRoot
		if workspace == "" {
			workspace = base.WorkspaceRoot(ctx.ProjectRoot)
		}
		arguments = append([]string{"--add-dir", workspace}, arguments...)
	}
	if ctx.Yolo {
		arguments = base.PrependOnce(arguments, "--dangerously-skip-permissions")
	}
	binary, err := base.Agy.Resolve()
	if err != nil {
		return nil, nil, err
	}
	return exec.Command(binary, arguments...), base.Done, nil
}
func stagedRoot(ctx base.StageContext) (string, error) {
	return paths.StagingBundleDir(ctx.ProjectRoot, ctx.Mode.Name(), "agy")
}
func resetPaths(ctx base.StageContext) ([]string, error) {
	root, err := paths.StagingSubdir(ctx.ProjectRoot, ctx.Mode.Name(), "agy")
	if err != nil {
		return nil, err
	}
	return []string{root}, nil
}
func prepare(ctx base.StageContext) error {
	bundle, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]string{"name": "agentpack-bundle"}, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "plugin.json"), data, 0o644); err != nil {
		return err
	}
	if keepAttribution() {
		return nil
	}
	rule := "---\ndescription: Disable AI attribution footers\ntrigger: always_on\n---\n\n" + strings.TrimSpace(base.NoAttributionBody) + "\n"
	path := filepath.Join(bundle, "rules", "agentpack-no-attribution.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rule), 0o644)
}
func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	data, warnings, err := RenderMCP(entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.NewMCPProjection(entries, base.MCPProjectedFile{
		Path: filepath.Join(root, "mcp_config.json"), Contents: data, Scope: "mcpServers",
		MergeOrder: append(base.MCPMergeOrder(), "Antigravity target projection"),
	}, warnings, nil, nil)
}
func verify(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "plugin.json")); err != nil {
		return fmt.Errorf("agy bundle missing %s: %w", root, err)
	}
	if ctx.LaunchTarget != nil && *ctx.LaunchTarget == base.Agy {
		entries, err := readOverlayManifest(ctx.ProjectRoot)
		if err != nil {
			return err
		}
		// Nothing recorded means the user's own directory was left alone; once it
		// is moved away the launch has to build the overlay.
		if len(entries) == 0 {
			if _, err := os.Lstat(overlayPath(ctx)); err != nil {
				return fmt.Errorf("agy workspace overlay missing: %w", err)
			}
		}
		bundle, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		for _, path := range entries {
			source, err := os.ReadFile(filepath.Join(path, OverlayMarker))
			if err != nil || string(source) != bundle {
				return fmt.Errorf("agy workspace overlay at %s is not a copy of %s", path, bundle)
			}
		}
	}
	return nil
}
func overlayPath(ctx base.StageContext) string {
	workspace := ctx.WorkspaceRoot
	if workspace == "" {
		var err error
		if workspace, err = os.Getwd(); err != nil {
			workspace = ctx.ProjectRoot
		}
	}
	return filepath.Join(workspace, filepath.FromSlash(workspaceOverlay))
}

// finalizeWorkspace copies the staged bundle into the workspace. agy ignores
// symlinks during workspace discovery, so a link would hide every skill.
func finalizeWorkspace(ctx base.StageContext) error {
	if ctx.StrictExternal {
		return fmt.Errorf("agy workspace overlay refused under --strict-external")
	}
	bundle, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(bundle, "plugin.json")); err != nil {
		return nil
	}
	if bundle, err = filepath.Abs(bundle); err != nil {
		return err
	}
	overlay := overlayPath(ctx)
	exists, owned, err := inspectOverlay(ctx.ProjectRoot, overlay)
	if err != nil {
		return err
	}
	if exists && !owned {
		warnUnowned()
		return writeOverlayManifest(ctx.ProjectRoot, nil)
	}
	agents := filepath.Dir(filepath.Dir(overlay))
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		return err
	}
	if err := removeOverlayLeftovers(agents); err != nil {
		return err
	}
	// Built outside plugins/, so agy never discovers a half-written copy.
	temporary, err := os.MkdirTemp(agents, OverlayTempPrefix+"*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := base.CopySelectedEntries(bundle, temporary, entriesIn(bundle)); err != nil {
		return err
	}
	for name, content := range map[string]string{".gitignore": "*\n", OverlayMarker: bundle} {
		if err := os.WriteFile(filepath.Join(temporary, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	// mcp_config.json can carry headers.
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	if exists {
		// Moved aside whole: a removal that failed halfway would leave a
		// directory without its marker, which no later launch may replace.
		old := temporary + "-old"
		if err := os.Rename(overlay, old); err != nil {
			return err
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(temporary, overlay); err != nil {
		return err
	}
	return writeOverlayManifest(ctx.ProjectRoot, []string{overlay})
}

// inspectOverlay reports whether something is at overlay and whether Agentpack
// made it: a link (what older versions wrote), a marked directory, or the
// unmarked copy an older version recorded where links were refused.
func inspectOverlay(projectRoot, overlay string) (exists, owned bool, err error) {
	info, err := os.Lstat(overlay)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true, true, nil
	}
	if !info.IsDir() {
		return true, false, nil
	}
	if _, err := os.Lstat(filepath.Join(overlay, OverlayMarker)); err == nil {
		return true, true, nil
	}
	recorded, err := readOverlayManifest(projectRoot)
	if err != nil || !slices.Contains(recorded, overlay) {
		return true, false, err
	}
	var plugin struct {
		Name string `json:"name"`
	}
	data, err := os.ReadFile(filepath.Join(overlay, "plugin.json"))
	return true, err == nil && json.Unmarshal(data, &plugin) == nil && plugin.Name == paths.StagedAgentpackBundleName, nil
}

func removeOverlayLeftovers(agents string) error {
	entries, err := os.ReadDir(agents)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), OverlayTempPrefix) {
			if err := os.RemoveAll(filepath.Join(agents, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func warnUnowned() {
	log.Print(unownedOverlay)
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return
	}
	spool, err := obs.OpenSpool(home, obs.SpoolOptions{Proc: "cli"})
	if err != nil {
		return
	}
	defer spool.Close()
	_, event := obs.Start(obs.WithSink(obs.ProcessTrace(context.Background()), spool, obs.BuildMetadata("", "local", "cli")), "harness", "agy workspace overlay")
	event.Fail(errors.New(unownedOverlay))
	event.End()
}

func entriesIn(root string) []string {
	entries, _ := os.ReadDir(root)
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.Name())
	}
	return values
}
func keepAttribution() bool {
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func readOverlayManifest(projectRoot string) ([]string, error) {
	path, err := OverlayManifestPath(projectRoot)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}
func writeOverlayManifest(projectRoot string, entries []string) error {
	path, err := OverlayManifestPath(projectRoot)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	sort.Strings(entries)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(entries, "\n")+"\n"), 0o644)
}
func OverlayManifestPath(projectRoot string) (string, error) {
	return paths.ProjectStateFile(projectRoot, "agy-overlay.manifest")
}
