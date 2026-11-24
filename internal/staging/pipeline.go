package staging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/grok"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type Pipeline struct {
	// ProjectRoot is the definition root (manifest, lock, staging identity).
	ProjectRoot string
	// WorkspaceRoot is the checkout for .agents inputs; empty means ProjectRoot.
	WorkspaceRoot string
	Lock          lockfile.PackLock
	Manifest      *manifest.Manifest
	Mode          mode.Effective
	Target        *base.Target
	// StrictExternal refuses workspace overlay materialization.
	StrictExternal bool
	// Info receives lines that report what staging did and need no action,
	// such as how each hook was rendered. Nil drops them.
	Info func(message string)
}

func (pipeline Pipeline) harnesses() []registry.Entry {
	all := registry.All()
	if pipeline.Target == nil {
		return all
	}
	return slices.DeleteFunc(all, func(candidate registry.Entry) bool { return candidate.Target != *pipeline.Target })
}

func (pipeline Pipeline) includes(target base.Target) bool {
	return pipeline.Target == nil || *pipeline.Target == target
}

func (pipeline Pipeline) workspace() string {
	if pipeline.WorkspaceRoot != "" {
		return pipeline.WorkspaceRoot
	}
	return pipeline.ProjectRoot
}

func (pipeline Pipeline) Rebuild() (_ []string, rebuildErr error) {
	ctx := pipeline.context()
	harnesses := pipeline.harnesses()
	managed, err := pipeline.managedRebuildPaths(ctx, harnesses)
	if err != nil {
		return nil, err
	}
	journalPath, err := pipeline.rebuildJournalPath()
	if err != nil {
		return nil, err
	}
	if err := recoverRebuildJournal(journalPath, managed); err != nil {
		return nil, fmt.Errorf("recover interrupted staging: %w", err)
	}
	for _, candidate := range registry.All() {
		if !pipeline.includes(candidate.Target) && !candidate.Target.UsesWorkspaceOverlay() {
			continue
		}
		if err := candidate.PreReset(ctx); err != nil {
			return nil, fmt.Errorf("pre-reset %s: %w", candidate.Target, err)
		}
	}
	var transactions []base.RebuildTransaction
	defer func() {
		for index := len(transactions) - 1; index >= 0; index-- {
			if err := transactions[index].Abort(); err != nil {
				rebuildErr = errors.Join(rebuildErr, err)
			}
		}
	}()
	ctx.StagedRoots = make(map[base.Target]string)
	for _, candidate := range harnesses {
		transaction, err := candidate.BeginRebuild(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin rebuild %s: %w", candidate.Target, err)
		}
		if transaction != nil {
			transactions = append(transactions, transaction)
			ctx.StagedRoots[candidate.Target] = transaction.Root()
		}
	}
	backups, err := backupRebuildPaths(managed, journalPath)
	if err != nil {
		return nil, err
	}
	published := false
	defer func() { rebuildErr = errors.Join(rebuildErr, finishRebuildJournal(journalPath, backups, published)) }()
	for _, candidate := range harnesses {
		if err := candidate.Setup(ctx); err != nil {
			return nil, fmt.Errorf("prepare %s: %w", candidate.Target, err)
		}
	}
	roots := make([]HarnessRoot, 0, len(harnesses))
	for _, candidate := range harnesses {
		root, err := candidate.Root(ctx)
		if err != nil {
			return nil, err
		}
		roots = append(roots, HarnessRoot{candidate.Target, root})
	}
	packagePlans, err := StagePackOverlay(pipeline.Lock, roots, pipeline.Mode)
	if err != nil {
		return nil, err
	}
	hookPlans, err := pipeline.stageHooks(ctx, harnesses)
	if err != nil {
		return nil, err
	}
	var mcpPluginPlan MCPPluginPlan
	dotPlan, err := PlanDotAgents(paths.ProjectDotAgentsDir(pipeline.workspace()), pipeline.Mode, pipeline.Target)
	if err != nil {
		return nil, err
	}
	plugins, err := paths.StagingPluginsDirForMode(pipeline.ProjectRoot, pipeline.Mode.Name())
	if err != nil {
		return nil, err
	}
	if err := ApplyDotAgentsPlan(dotPlan, roots); err != nil {
		return nil, err
	}
	bundle := filepath.Join(plugins, paths.StagedAgentpackBundleName)
	for i, candidate := range harnesses {
		if candidate.ProjectSkillsShadow {
			projectSkills := filepath.Join(pipeline.workspace(), filepath.FromSlash(candidate.ProjectSkillRoot))
			if err := omitProjectSkillDuplicates(projectSkills, roots[i].Path, base.DisplayName(candidate.Target)); err != nil {
				return nil, err
			}
		}
	}
	mcpPlan, err := CollectMCPPlan(pipeline.workspace(), pipeline.Lock, pipeline.Manifest, &pipeline.Mode)
	if err != nil {
		return nil, err
	}
	for _, warning := range mcpPlan.Warnings {
		log.Printf("warning: %s", warning)
	}
	merged := mcpPlan.Entries
	if merged, err = mcp.ApplyTrees(merged, pipeline.Lock.MCPServers, hooks.Executable(), filepath.Join(bundle, "mcp-trees")); err != nil {
		return nil, err
	}
	merged = mcp.ApplyPins(merged, pipeline.Lock.MCPServers)
	mcpPluginPlan, err = PlanMCPPluginRoots(pipeline.ProjectRoot, pipeline.Mode.Name(), pipeline.Mode, merged)
	if err != nil {
		return nil, err
	}
	if err := ApplyMCPPluginPlan(mcpPluginPlan); err != nil {
		return nil, err
	}
	merged = mcpPluginPlan.Entries
	if merged, err = applyMCPCredentials(pipeline.ProjectRoot, merged); err != nil {
		return nil, err
	}
	var mcpProjections []base.MCPProjection
	if len(merged) != 0 {
		for _, candidate := range harnesses {
			projection, err := candidate.PlanMCP(merged, ctx)
			if err != nil {
				return nil, fmt.Errorf("plan MCP %s: %w", candidate.Target, err)
			}
			if err := base.WriteMCPProjection(projection); err != nil {
				return nil, fmt.Errorf("write MCP %s: %w", candidate.Target, err)
			}
			mcpProjections = append(mcpProjections, projection)
		}
	}
	declaredServers, err := CollectLSP(pipeline.Lock, &pipeline.Mode)
	if err != nil {
		return nil, err
	}
	languageServers, err := pipeline.stageLSPRoots(declaredServers)
	if err != nil {
		return nil, err
	}
	if len(languageServers) != 0 {
		names := make([]string, 0, len(languageServers))
		for name := range languageServers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, candidate := range harnesses {
			projection, err := candidate.PlanLSP(languageServers, ctx)
			if errors.Is(err, base.ErrNoLSP) {
				log.Printf("warning: skipping language server(s) %s for %s: it has no language server setting", strings.Join(names, ", "), base.DisplayName(candidate.Target))
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("plan language servers %s: %w", candidate.Target, err)
			}
			for _, warning := range projection.Warnings {
				log.Printf("warning: %s", warning)
			}
			if err := base.WriteLSPProjection(projection); err != nil {
				return nil, fmt.Errorf("write language servers %s: %w", candidate.Target, err)
			}
		}
	}
	guidance, err := CollectGuidance(pipeline.workspace(), pipeline.Lock, pipeline.Mode)
	if err != nil {
		return nil, err
	}
	if guidance != "" {
		for _, candidate := range harnesses {
			if err := candidate.InjectGuidance(guidance, ctx); err != nil {
				return nil, fmt.Errorf("inject guidance %s: %w", candidate.Target, err)
			}
		}
	}
	for _, candidate := range harnesses {
		if err := candidate.Finalize(merged, ctx); err != nil {
			return nil, fmt.Errorf("finalize %s: %w", candidate.Target, err)
		}
	}
	overlayChangesDotAgents := false
	if pipeline.Target != nil {
		if pipeline.StrictExternal && pipeline.Target.UsesWorkspaceOverlay() {
			return nil, fmt.Errorf("%s requires a workspace overlay write; refuse under --strict-external (use claude/opencode, or omit --strict-external)", *pipeline.Target)
		}
		candidate, err := registry.ByTarget(*pipeline.Target)
		if err != nil {
			return nil, err
		}
		if err := dotPlan.Validate(); err != nil {
			return nil, err
		}
		overlayChangesDotAgents = slices.ContainsFunc(candidate.Inheritance.Overlay, func(path string) bool {
			return strings.HasPrefix(path, ".agents/")
		})
		if err := candidate.FinalizeWorkspaceOverlay(ctx); err != nil {
			return nil, err
		}
	}
	home, _ := os.UserHomeDir()
	for _, candidate := range harnesses {
		root, err := candidate.Root(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := ResolveCollisions(root, string(candidate.Target), candidate.NativeUserRoots(home)); err != nil {
			return nil, err
		}
	}
	if err := pipeline.verify(ctx); err != nil {
		return nil, err
	}
	if err := pipeline.recordStaged(ctx); err != nil {
		return nil, fmt.Errorf("record staged content: %w", err)
	}
	for _, plan := range packagePlans {
		if err := plan.Validate(); err != nil {
			return nil, err
		}
	}
	// Overlays written under .agents have already had their inputs checked
	// immediately before they changed that tree.
	if !overlayChangesDotAgents {
		if err := dotPlan.Validate(); err != nil {
			return nil, err
		}
	}
	if err := mcpPluginPlan.Validate(); err != nil {
		return nil, err
	}
	if err := base.ValidateMCPProjectionInputs(mcpProjections); err != nil {
		return nil, err
	}
	for _, plan := range hookPlans {
		if err := plan.Validate(); err != nil {
			return nil, err
		}
	}
	for _, transaction := range transactions {
		if err := transaction.Commit(); err != nil {
			return nil, fmt.Errorf("publish staged home: %w", err)
		}
	}
	published = true
	MarkProjectDirectories(pipeline.ProjectRoot)
	return []string{filepath.Join(plugins, paths.StagedAgentpackBundleName)}, nil
}

func (pipeline Pipeline) Verify() error {
	return pipeline.verify(pipeline.context())
}

func (pipeline Pipeline) verify(ctx base.StageContext) error {
	harnesses := pipeline.harnesses()
	for _, candidate := range harnesses {
		if err := candidate.Check(ctx); err != nil {
			return fmt.Errorf("verify %s: %w", candidate.Target, err)
		}
	}
	if err := pipeline.verifyPluginRoots(); err != nil {
		return err
	}
	plugins, err := paths.StagingPluginsDirForMode(pipeline.ProjectRoot, pipeline.Mode.Name())
	if err != nil {
		return err
	}
	if _, err := os.ReadDir(plugins); err != nil && !os.IsNotExist(err) {
		return err
	}
	var skillRoots []string
	nativeSkills := make(map[base.Target]map[string]struct{})
	home, _ := os.UserHomeDir()
	for _, candidate := range harnesses {
		root, err := candidate.Root(ctx)
		if err != nil {
			return err
		}
		skillRoots = append(skillRoots, root)
		names, err := collisions(root, string(candidate.Target), candidate.NativeUserRoots(home), false)
		if err != nil {
			return err
		}
		if candidate.ProjectSkillsShadow {
			projectNames, err := ShadowingProjectSkillNames(pipeline.workspace(), candidate.ProjectSkillRoot)
			if err != nil {
				return err
			}
			maps.Copy(names, projectNames)
		}
		nativeSkills[candidate.Target] = names
	}
	pluginPackages := pipeline.Lock.Plugins()
	for _, skill := range pipeline.Lock.Skills() {
		if disabledPlugin(pipeline.Lock, skill.CacheKey) || SkillIsShadowed(skill, pluginPackages) {
			continue
		}
		allowed, err := pipeline.Mode.AllowsPackagePath(skill.Module, "SKILL.md")
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		cacheRoot, err := cache.EntryDir(skill.CacheKey)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(cacheRoot, "SKILL.md")); err != nil {
			continue
		}
		name := SkillFolderName(skill)
		for index, root := range skillRoots {
			if _, native := nativeSkills[harnesses[index].Target][strings.ToLower(name)]; native {
				continue
			}
			path := filepath.Join(root, "skills", name, "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("%s staging missing skill SKILL.md %s", harnesses[index].Target, path)
			}
		}
	}
	return nil
}

func (pipeline Pipeline) context() base.StageContext {
	return base.StageContext{
		ProjectRoot:    pipeline.ProjectRoot,
		WorkspaceRoot:  pipeline.workspace(),
		Mode:           pipeline.Mode,
		LaunchTarget:   pipeline.Target,
		StrictExternal: pipeline.StrictExternal,
	}
}

// PlanHooks computes the hook files each selected harness would receive.
func (pipeline Pipeline) PlanHooks() ([]hooks.TargetPlan, error) {
	harnesses := pipeline.harnesses()
	bundle, err := pipeline.collectHooks(harnesses)
	if err != nil {
		return nil, err
	}
	return planHookTargets(bundle, pipeline.context(), harnesses)
}

func (pipeline Pipeline) collectHooks(harnesses []registry.Entry) (hooks.Bundle, error) {
	var seeds []hooks.Seed
	for _, candidate := range harnesses {
		if candidate.SeededHooks == "" {
			continue
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return hooks.Bundle{}, err
		}
		seeds = append(seeds, hooks.Seed{Target: candidate.Target, Path: filepath.Join(home, filepath.FromSlash(candidate.SeededHooks))})
	}
	return hooks.Collect(pipeline.workspace(), pipeline.Lock, seeds, pipeline.Mode)
}

func planHookTargets(bundle hooks.Bundle, ctx base.StageContext, harnesses []registry.Entry) ([]hooks.TargetPlan, error) {
	var plans []hooks.TargetPlan
	for _, candidate := range harnesses {
		if candidate.Hooks == nil {
			continue
		}
		root, err := candidate.Root(ctx)
		if err != nil {
			return nil, err
		}
		plan, err := hooks.PlanTarget(bundle, candidate.Target, candidate.Hooks, hooks.RenderContext{ProjectRoot: ctx.ProjectRoot, TargetRoot: root}, ctx.Mode)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (pipeline Pipeline) stageHooks(ctx base.StageContext, harnesses []registry.Entry) ([]hooks.TargetPlan, error) {
	bundle, err := pipeline.collectHooks(harnesses)
	if err != nil {
		return nil, err
	}
	for _, diagnostic := range bundle.Skipped {
		fmt.Fprintf(os.Stderr, "warning: %s: %s\n", diagnostic.Source, diagnostic.Message)
	}
	if len(bundle.Hooks) == 0 {
		return nil, nil
	}
	plans, err := planHookTargets(bundle, ctx, harnesses)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		for _, diagnostic := range plan.Diagnostics {
			// A hook rendered as intended is information; one that was
			// left out or lost something is a warning.
			if diagnostic.Level == "native" || diagnostic.Level == "emulated" {
				if pipeline.Info != nil {
					pipeline.Info(fmt.Sprintf("hook for %s: %s: %s", plan.Target, diagnostic.Source, diagnostic.Message))
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", diagnostic.Source, diagnostic.Message)
		}
		if err := hooks.ApplyTargetPlan(plan); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

// A durable journal records original paths before any rename. Production callers
// hold the rebuild-exclusive lock; launch fast paths refuse pending journals.
type rebuildBackup struct {
	Path      string `json:"path"`
	Directory string `json:"directory,omitempty"`
	Existed   bool   `json:"existed"`
}
type rebuildJournal struct {
	SchemaVersion int             `json:"schema_version"`
	Committed     bool            `json:"committed"`
	Backups       []rebuildBackup `json:"backups"`
}

func (pipeline Pipeline) rebuildJournalPath() (string, error) {
	root, err := paths.StagingRootForMode(pipeline.ProjectRoot, pipeline.Mode.Name())
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".rebuild.json"), nil
}

// RebuildPending tells launchers to restore before using staging after an interruption.
func (pipeline Pipeline) RebuildPending() (bool, error) {
	path, err := pipeline.rebuildJournalPath()
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (pipeline Pipeline) managedRebuildPaths(ctx base.StageContext, harnesses []registry.Entry) ([]string, error) {
	var managed []string
	for _, candidate := range harnesses {
		// A private rebuild transaction leaves the published root in place.
		if candidate.Begin != nil {
			continue
		}
		reset, err := candidate.ResetPaths(ctx)
		if err != nil {
			return nil, err
		}
		managed = append(managed, reset...)
		root, err := candidate.Root(ctx)
		if err != nil {
			return nil, err
		}
		managed = append(managed, root)
	}
	if pipeline.includes(base.Grok) {
		home, err := grok.StagingHome(pipeline.ProjectRoot, pipeline.Mode.Name())
		if err != nil {
			return nil, err
		}
		managed = append(managed, home)
	}
	pluginRoots, err := paths.StagingPluginRootsDirForMode(pipeline.ProjectRoot, pipeline.Mode.Name())
	if err != nil {
		return nil, err
	}
	managed = append(managed, pluginRoots)
	manifestPath, err := paths.StagedManifestPath(pipeline.ProjectRoot, pipeline.Mode.Name())
	if err != nil {
		return nil, err
	}
	managed = append(managed, manifestPath)
	return normalizeRebuildPaths(managed), nil
}

func normalizeRebuildPaths(values []string) []string {
	for index, path := range values {
		values[index] = filepath.Clean(path)
	}
	sort.Strings(values)
	var result []string
	for _, path := range slices.Compact(values) {
		nested := false
		for _, parent := range result {
			if strings.HasPrefix(path, parent+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested {
			result = append(result, path)
		}
	}
	return result
}

func writeRebuildJournal(path string, journal rebuildJournal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return paths.WriteAtomic(path, data, 0o600)
}

func backupRebuildPaths(values []string, journalPath string) ([]rebuildBackup, error) {
	var backups []rebuildBackup
	for _, path := range normalizeRebuildPaths(values) {
		backup := rebuildBackup{Path: path}
		_, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, errors.Join(err, finishRebuildBackups(backups, true))
		}
		if err == nil {
			backup.Existed = true
			backup.Directory, err = os.MkdirTemp(filepath.Dir(path), ".agentpack-rebuild-backup-*")
			if err != nil {
				return nil, errors.Join(err, finishRebuildBackups(backups, true))
			}
		}
		backups = append(backups, backup)
	}
	if err := writeRebuildJournal(journalPath, rebuildJournal{SchemaVersion: 1, Backups: backups}); err != nil {
		return nil, errors.Join(err, finishRebuildBackups(backups, true))
	}
	for _, backup := range backups {
		if backup.Existed {
			if err := os.Rename(backup.Path, filepath.Join(backup.Directory, "previous")); err != nil {
				return nil, errors.Join(err, finishRebuildJournal(journalPath, backups, false))
			}
		}
	}
	return backups, nil
}

func recoverRebuildJournal(path string, managed []string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("invalid staging recovery journal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var journal rebuildJournal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return err
	}
	if journal.SchemaVersion != 1 {
		return fmt.Errorf("unsupported rebuild journal schema")
	}
	allowed := map[string]bool{}
	for _, value := range managed {
		allowed[value] = true
	}
	seen := map[string]bool{}
	if len(journal.Backups) != len(allowed) {
		return fmt.Errorf("recovery journal does not enumerate every managed path")
	}
	for _, backup := range journal.Backups {
		if !allowed[backup.Path] || seen[backup.Path] {
			return fmt.Errorf("recovery journal references an unexpected managed path")
		}
		seen[backup.Path] = true
		if backup.Existed {
			if filepath.Dir(backup.Directory) != filepath.Dir(backup.Path) || !strings.HasPrefix(filepath.Base(backup.Directory), ".agentpack-rebuild-backup-") {
				return fmt.Errorf("invalid staging backup location")
			}
			if info, err := os.Lstat(backup.Directory); err == nil {
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("staging backup must be a private directory")
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		} else if backup.Directory != "" {
			return fmt.Errorf("unexpected staging backup for absent path")
		}
	}
	return finishRebuildJournal(path, journal.Backups, journal.Committed)
}

func finishRebuildJournal(path string, backups []rebuildBackup, published bool) error {
	if published {
		if err := writeRebuildJournal(path, rebuildJournal{SchemaVersion: 1, Committed: true, Backups: backups}); err != nil {
			return fmt.Errorf("staging published but recovery marker could not be saved: %w", err)
		}
	}
	if err := finishRebuildBackups(backups, published); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func finishRebuildBackups(backups []rebuildBackup, published bool) error {
	var failures []error
	for index := len(backups) - 1; index >= 0; index-- {
		backup := backups[index]
		if !published {
			restore := !backup.Existed
			if backup.Existed {
				if _, err := os.Lstat(filepath.Join(backup.Directory, "previous")); err == nil {
					restore = true
				} else if !os.IsNotExist(err) {
					failures = append(failures, err)
					continue
				} else if _, originalErr := os.Lstat(backup.Path); originalErr != nil {
					failures = append(failures, fmt.Errorf("prior staging and backup are missing for %s; journal retained", backup.Path))
					continue
				}
			}
			// A missing prior tree means interruption occurred before its rename, or
			// rollback already restored it. Never remove that surviving original.
			if restore {
				if err := os.RemoveAll(backup.Path); err != nil {
					failures = append(failures, fmt.Errorf("rollback %s (backup retained at %s): %w", backup.Path, backup.Directory, err))
					continue
				}
				if backup.Existed {
					if err := os.Rename(filepath.Join(backup.Directory, "previous"), backup.Path); err != nil {
						failures = append(failures, fmt.Errorf("restore %s (backup retained at %s): %w", backup.Path, backup.Directory, err))
						continue
					}
				}
			}
		}
		if backup.Directory != "" {
			if err := os.RemoveAll(backup.Directory); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
