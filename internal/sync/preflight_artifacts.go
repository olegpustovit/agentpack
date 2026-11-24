package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
	"github.com/pelletier/go-toml/v2"
)

// RedactCredentialArgs removes flag credentials and private path prefixes.
func RedactCredentialArgs(args []string) []string {
	result := slices.Clone(args)
	redactNext := false
	for i, arg := range result {
		lower := strings.ToLower(arg)
		if redactNext {
			result[i] = "<redacted>"
			redactNext = false
			continue
		}
		if strings.HasPrefix(arg, "-") && credentialFlag(lower) {
			if strings.Contains(arg, "=") {
				result[i] = arg[:strings.IndexByte(arg, '=')+1] + "<redacted>"
			} else {
				redactNext = true
			}
			continue
		}
		result[i] = portablePathValue(arg)
	}
	return result
}

func portablePathValue(value string) string {
	if isHostAbsolutePath(value) {
		return filepath.Base(filepath.FromSlash(strings.ReplaceAll(value, `\`, "/")))
	}
	return value
}

func isHostAbsolutePath(value string) bool {
	return filepath.IsAbs(value) || strings.HasPrefix(value, "/") || isWindowsAbsolutePath(value)
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func credentialFlag(value string) bool {
	for _, marker := range []string{"token", "secret", "password", "passwd", "api-key", "api_key", "credential", "authorization"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func redactCredentialURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("<redacted>", "<redacted>")
	}
	if parsed.RawQuery != "" {
		parts := strings.Split(parsed.RawQuery, "&")
		for i, part := range parts {
			key, _, found := strings.Cut(part, "=")
			if found && credentialFlag(strings.ToLower(key)) {
				parts[i] = key + "=<redacted>"
			}
		}
		parsed.RawQuery = strings.Join(parts, "&")
	}
	return parsed.String()
}

func canonicalStrings(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return result
}

func plannedArtifacts(lock lockfile.PackLock, effective mode.Effective, definitionRoot, workspace string, target *base.Target, cacheReady bool) ([]environment.ArtifactRecord, []hooks.TargetPlan, error) {
	records, err := plannedPackageArtifacts(lock, effective, target, cacheReady)
	if err != nil {
		return nil, nil, err
	}
	records, err = appendDotAgentsArtifacts(records, paths.ProjectDotAgentsDir(workspace), effective, target)
	if err != nil {
		return nil, nil, err
	}
	var hookPlans []hooks.TargetPlan
	if cacheReady {
		pipeline := staging.Pipeline{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Lock: lock, Mode: effective, Target: target}
		plans, err := pipeline.PlanHooks()
		if err != nil {
			return nil, nil, err
		}
		hookPlans = plans
		hookRecords, err := hookArtifactRecords(hookPlans)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, hookRecords...)
		mcpRecords, err := plannedMCPArtifacts(lock, effective, definitionRoot, workspace, target)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, mcpRecords...)
	}
	return records, hookPlans, nil
}

func plannedPackageArtifacts(lock lockfile.PackLock, effective mode.Effective, target *base.Target, cacheReady bool) ([]environment.ArtifactRecord, error) {
	var records []environment.ArtifactRecord
	plugins := lock.Plugins()
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].CacheKey < plugins[j].CacheKey })
	skills := lock.Skills()
	sort.Slice(skills, func(i, j int) bool { return skills[i].CacheKey < skills[j].CacheKey })
	for _, plugin := range plugins {
		if !cacheReady {
			records = append(records, environment.ArtifactRecord{ID: plugin.Module, Kind: "plugin", Name: plugin.Name, Module: plugin.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Omitted: true, Reason: "locked cache unavailable", Properties: map[string]any{"source_digest": plugin.ContentHash}})
			continue
		}
		root, err := cache.EntryDir(plugin.CacheKey)
		if err != nil {
			return nil, err
		}
		name := plugin.Name
		if name == "" {
			name = staging.SkillFolderName(plugin)
		}
		record := environment.ArtifactRecord{ID: plugin.Module, Kind: "plugin", Name: name, Module: plugin.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Properties: map[string]any{"source_digest": plugin.ContentHash}}
		disabled := slices.Contains(lock.Config.DisabledPlugins, plugin.CacheKey)
		if disabled {
			record.Omitted = true
			record.Reason = "disabled by lock"
		}
		records = append(records, record)
		records, err = appendPackageTreeArtifacts(records, root, plugin.Module, "", !disabled, effective, target)
		if err != nil {
			return nil, err
		}
	}
	for _, skill := range skills {
		if !cacheReady {
			name := staging.SkillFolderName(skill)
			records = append(records, environment.ArtifactRecord{ID: skill.Module, Kind: "skill", Name: name, Module: skill.Module, Winner: "package", Evidence: environment.EvidenceDeclared, Omitted: true, Reason: "locked cache unavailable", Properties: map[string]any{"source_digest": skill.ContentHash}})
			continue
		}
		root, err := cache.EntryDir(skill.CacheKey)
		if err != nil {
			return nil, err
		}
		enabled := !slices.Contains(lock.Config.DisabledPlugins, skill.CacheKey) && !staging.SkillIsShadowed(skill, plugins)
		records, err = appendPackageTreeArtifacts(records, root, skill.Module, staging.SkillFolderName(skill), enabled, effective, target)
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func appendDotAgentsArtifacts(records []environment.ArtifactRecord, root string, effective mode.Effective, target *base.Target) ([]environment.ArtifactRecord, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return records, nil
	} else if err != nil {
		return nil, err
	}
	const module = ".agents"
	plan, err := staging.PlanDotAgents(root, effective, target)
	if err != nil {
		return nil, err
	}
	for _, item := range plan.Artifacts {
		kind := map[artifacts.Kind]string{artifacts.Skill: "skill", artifacts.Command: "command", artifacts.Agent: "agent", artifacts.Rule: "rule"}[item.Kind]
		targetName := string(item.Target)
		record := environment.ArtifactRecord{
			ID: module + ":" + item.SourceRelative + ":" + targetName, Kind: kind, Name: item.Name,
			Module: module, Winner: "project", Evidence: environment.EvidenceGenerated,
			Properties: map[string]any{"source_digest": digestBytes(item.Source), "source_kind": kind, "target": targetName},
		}
		if item.NativeSource != "" {
			record.Evidence = environment.EvidenceDeclared
			record.Properties["native_source_path"] = filepath.ToSlash(filepath.Join(".agents", item.NativeSource))
		} else if item.Omitted {
			record.Omitted, record.Reason = true, item.Reason
		} else if item.OutputPath != "" {
			record.OutputPath = item.OutputPath
			record.Properties["output_path"] = item.OutputPath
			record.Properties["rendered_digest"] = digestBytes(item.Output)
		}
		if strings.HasPrefix(record.OutputPath, "skills/") {
			record.Kind = "skill"
		}
		records = appendPlannedArtifact(records, record)
	}
	return records, nil
}

func appendPackageTreeArtifacts(records []environment.ArtifactRecord, root, module, bareName string, enabled bool, effective mode.Effective, target *base.Target) ([]environment.ArtifactRecord, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return records, nil
	} else if err != nil {
		return nil, err
	}
	if bareName == "" && !cache.HasPluginManifest(root) {
		return records, nil
	}
	if bareName != "" {
		if info, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
			return records, nil
		}
	}
	var roots []staging.HarnessRoot
	if target != nil {
		roots = append(roots, staging.HarnessRoot{Target: *target})
	}
	plan, err := staging.PlanPackageTree(root, module, bareName, effective, roots, bareName == "")
	if err != nil {
		return nil, err
	}
	for _, planned := range plan.Artifacts {
		kind := map[artifacts.Kind]string{artifacts.Skill: "skill", artifacts.Command: "command", artifacts.Agent: "agent", artifacts.Rule: "rule"}[planned.Kind]
		record := environment.ArtifactRecord{ID: module + ":" + planned.SourceRelative, Kind: kind, Name: planned.Parsed.StorageName, Module: module, Winner: "package", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"source_digest": digestBytes(planned.Source), "source_kind": kind}}
		if len(planned.AdditionalOutputs) > 0 {
			record.Properties["additional_outputs"] = slices.Clone(planned.AdditionalOutputs)
		}
		if !enabled || planned.Omitted {
			record.Omitted = true
			record.Reason = planned.Reason
			if record.Reason == "" {
				record.Reason = "disabled by mode or lock"
			}
		}
		if target != nil && !record.Omitted {
			record.OutputPath = planned.OutputPath
			record.Properties["output_path"] = planned.OutputPath
			record.Properties["rendered_digest"] = digestBytes(planned.Output)
			if planned.Kind == artifacts.Rule && strings.HasPrefix(planned.OutputPath, "skills/") && len(planned.Parsed.Globs) > 0 {
				record.Properties["scope_preserved"] = false
			}
			projected, parseErr := artifacts.Parse(planned.OutputPath, string(planned.Output), "")
			if parseErr != nil {
				return nil, parseErr
			}
			var dropped []string
			for key := range planned.Parsed.ExtraFrontmatter {
				if projected == nil {
					dropped = append(dropped, key)
				} else if _, ok := projected.ExtraFrontmatter[key]; !ok {
					dropped = append(dropped, key)
				}
			}
			sort.Strings(dropped)
			if len(dropped) > 0 {
				record.Properties["dropped_fields"] = dropped
			}
		}
		if strings.HasPrefix(record.OutputPath, "skills/") {
			record.Kind = "skill"
		}
		records = appendPlannedArtifact(records, record)
	}
	return records, nil
}

func appendPlannedArtifact(records []environment.ArtifactRecord, record environment.ArtifactRecord) []environment.ArtifactRecord {
	if !record.Omitted {
		for index := range records {
			prior := &records[index]
			if recordTarget, ok := record.Properties["target"].(string); ok {
				if priorTarget, priorHasTarget := prior.Properties["target"].(string); priorHasTarget && priorTarget != recordTarget {
					continue
				}
			}
			same := prior.Kind == record.Kind && prior.Name == record.Name
			if record.OutputPath != "" {
				same = prior.OutputPath == record.OutputPath
			}
			if same && !prior.Omitted {
				prior.Omitted = true
				prior.Winner = record.Winner
				prior.Reason = "overridden by " + record.ID
			}
			for _, output := range recordOutputPaths(record) {
				for _, priorOutput := range recordOutputPaths(*prior) {
					if output == priorOutput {
						if output == record.OutputPath && output == prior.OutputPath {
							continue
						}
						appendArtifactPropertyString(prior, "overwritten_outputs", output)
						appendArtifactPropertyString(&record, "overrides_outputs", output)
					}
				}
			}
		}
	}
	return append(records, record)
}

func plannedMCPArtifacts(lock lockfile.PackLock, effective mode.Effective, definitionRoot, workspace string, target *base.Target) ([]environment.ArtifactRecord, error) {
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return nil, err
	}
	collection, err := staging.CollectMCPPlan(workspace, lock, project, &effective)
	if err != nil {
		return nil, err
	}
	entries := collection.Entries
	bundle, err := paths.StagingPluginsDirForMode(definitionRoot, effective.Name())
	if err != nil {
		return nil, err
	}
	entries, treeFiles, err := mcp.PlanTrees(entries, lock.MCPServers, hooks.Executable(), filepath.Join(bundle, paths.StagedAgentpackBundleName, "mcp-trees"))
	if err != nil {
		return nil, err
	}
	entries = mcp.ApplyPins(entries, lock.MCPServers)
	pluginPlan, err := staging.PlanMCPPluginRoots(definitionRoot, effective.Name(), effective, entries)
	if err != nil {
		return nil, err
	}
	entries = pluginPlan.Entries
	var records []environment.ArtifactRecord
	for index, warning := range collection.Warnings {
		records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("mcp-omission:%d", index), Kind: "mcp_omission", Name: warning.Source, Winner: "omitted", Omitted: true, Reason: warning.Message, Evidence: environment.EvidenceGenerated})
	}
	for _, source := range pluginPlan.Sources {
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-root:" + source.Module, Kind: "mcp_plugin_root", Name: source.Module, Module: source.Module, Winner: "package", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"source_digest": source.Digest}})
	}
	for _, file := range pluginPlan.Files {
		path := filepath.ToSlash(filepath.Join("plugin-roots", filepath.Base(filepath.Dir(file.Path)), filepath.Base(file.Path)))
		for _, root := range pluginPlan.Roots {
			if relative, err := filepath.Rel(root, file.Path); err == nil && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				path = filepath.ToSlash(filepath.Join("plugin-roots", filepath.Base(root), relative))
				break
			}
		}
		properties := map[string]any{"target": "mcp_plugin_root", "mode": file.Mode.Perm()}
		if file.LinkTarget != "" {
			properties["link_target"] = file.LinkTarget
		} else {
			properties["rendered_digest"] = digestBytes(file.Contents)
		}
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-file:" + file.Module + ":" + path, Kind: "mcp_plugin_file", Name: filepath.Base(path), Module: file.Module, Winner: "package", OutputPath: path, Evidence: environment.EvidenceGenerated, Properties: properties})
	}
	for _, directory := range pluginPlan.DataDirs {
		records = append(records, environment.ArtifactRecord{ID: "mcp-plugin-data:" + filepath.Base(directory), Kind: "mcp_plugin_data_dir", Name: filepath.Base(directory), Winner: "plugin", OutputPath: filepath.ToSlash(filepath.Join("plugin-data", filepath.Base(directory))), Evidence: environment.EvidenceGenerated})
	}
	for _, file := range treeFiles {
		relative, err := filepath.Rel(filepath.Join(bundle, paths.StagedAgentpackBundleName), file.Path)
		if err != nil || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("planned MCP tree output escapes bundle: %s", file.Path)
		}
		relative = filepath.ToSlash(relative)
		records = append(records, environment.ArtifactRecord{ID: "mcp-tree:" + relative, Kind: "mcp_tree", Name: filepath.Base(relative), Winner: "lock", OutputPath: relative, Evidence: environment.EvidenceGenerated, Properties: map[string]any{"rendered_digest": digestBytes(file.Contents)}})
	}
	targets := base.AllTargets()
	if target != nil {
		targets = []base.Target{*target}
	}
	targetSeedConfigs := make(map[base.Target][]byte)
	for _, current := range targets {
		candidate, err := registry.ByTarget(current)
		if err != nil {
			return nil, err
		}
		ctx := base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective}
		if seedPath, label := inheritedConfigSeed(candidate.Harness); seedPath != "" {
			seed, err := os.ReadFile(seedPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			properties := map[string]any{"source": label, "exists": err == nil, "merge_order": []string{"native user seed retained", "agentpack-owned sections overlaid"}}
			if err == nil {
				properties["source_digest"] = digestBytes(seed)
				shape, err := redactedSeedShape(seedPath, seed)
				if err != nil {
					return nil, err
				}
				properties["ordered_values"] = shape
			}
			records = append(records, environment.ArtifactRecord{ID: "settings-seed:" + string(current), Kind: "settings_seed", Name: label, Winner: "user_seed", Evidence: environment.EvidenceGenerated, Properties: properties})
		}
		if candidate.PlannedSettings != nil {
			settings, err := candidate.PlannedSettings(entries)
			if err != nil {
				return nil, err
			}
			record := environment.ArtifactRecord{
				ID: "settings:" + string(current) + ":agentpack-overlay", Kind: "settings_file",
				Name: filepath.Base(settings.RelativePath), OutputPath: settings.RelativePath,
				Winner: "agentpack_overlay", Evidence: environment.EvidenceGenerated,
				Properties: map[string]any{"target": string(current), "merge_order": settings.MergeOrder},
			}
			if settings.Section != "" {
				record.ID = "settings:" + string(current) + ":" + settings.Section
				record.Kind = "settings_section"
				record.Name = settings.Section
				record.Properties["scope"] = settings.Section
				record.Properties["scope_digest"] = settings.SectionDigest
			} else {
				record.Properties["rendered_digest"] = digestBytes(settings.Contents)
			}
			records = append(records, record)
		}
		projection := base.MCPProjection{}
		if len(entries) != 0 {
			projection, err = candidate.PlanMCP(entries, ctx)
			if err != nil {
				return nil, fmt.Errorf("plan MCP %s: %w", current, err)
			}
		}
		for _, file := range projection.Files {
			targetSeedConfigs[current] = append([]byte(nil), file.Contents...)
		}
		for _, server := range projection.Servers {
			properties := map[string]any{"target": string(current), "source": server.Source}
			if len(server.Dropped) != 0 {
				properties["dropped_fields"] = slices.Clone(server.Dropped)
			}
			var warnings []string
			for _, warning := range projection.Warnings {
				if strings.HasPrefix(warning, "MCP server "+server.Name+": ") {
					warnings = append(warnings, warning)
				}
			}
			if len(warnings) != 0 {
				properties["warnings"] = warnings
			}
			records = append(records, environment.ArtifactRecord{ID: "mcp:" + server.Name + ":" + string(current), Kind: "mcp_server", Name: server.Name, Module: server.Module, Winner: server.Winner, Evidence: environment.EvidenceGenerated, Properties: properties})
		}
		for _, file := range projection.Files {
			name := filepath.Base(file.Path)
			if strings.Contains(filepath.ToSlash(file.Path), "/.cursor/") {
				name = "fake-home/.cursor/" + name
			}
			records = append(records, environment.ArtifactRecord{ID: "mcp-file:" + string(current) + ":" + name, Kind: "mcp_file", Name: name, Winner: "merged", OutputPath: name, Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "scope": file.Scope, "scope_digest": file.ScopeDigest, "merge_order": slices.Clone(file.MergeOrder)}})
		}
	}
	lspPlan, err := staging.CollectLSPPlan(lock, &effective)
	if err != nil {
		return nil, err
	}
	for index, warning := range lspPlan.Warnings {
		records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("lsp-source-omission:%d", index), Kind: "lsp_omission", Name: warning.Source, Winner: "omitted", Omitted: true, Reason: warning.Message, Evidence: environment.EvidenceGenerated})
	}
	lspServers, err := staging.PlanLSPRoots(definitionRoot, effective.Name(), lspPlan.Entries)
	if err != nil {
		return nil, err
	}
	for _, current := range targets {
		candidate, err := registry.ByTarget(current)
		if err != nil {
			return nil, err
		}
		projection, err := candidate.PlanLSP(lspServers, base.StageContext{ProjectRoot: definitionRoot, WorkspaceRoot: workspace, Mode: effective, SeedConfig: targetSeedConfigs[current]})
		if errors.Is(err, base.ErrNoLSP) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plan LSP %s: %w", current, err)
		}
		for index, warning := range projection.Warnings {
			records = append(records, environment.ArtifactRecord{ID: fmt.Sprintf("lsp-warning:%s:%d", current, index), Kind: "lsp_omission", Name: string(current), Winner: "omitted", Omitted: true, Reason: warning, Evidence: environment.EvidenceGenerated})
		}
		for _, name := range projection.Omitted {
			records = append(records, environment.ArtifactRecord{ID: "lsp:" + string(current) + ":" + name, Kind: "lsp_server", Name: name, Winner: "user", Omitted: true, Reason: "existing target configuration wins", Evidence: environment.EvidenceGenerated})
		}
		for _, file := range projection.Files {
			records = append(records, environment.ArtifactRecord{ID: "lsp-file:" + string(current) + ":" + filepath.Base(file.Path), Kind: "lsp_file", Name: filepath.Base(file.Path), Winner: "merged", OutputPath: filepath.Base(file.Path), Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "scope": file.Scope, "rendered_digest": digestBytes(file.Contents)}})
		}
	}
	return records, nil
}

func redactedSeedShape(path string, data []byte) (any, error) {
	var value any
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		if err := toml.Unmarshal(data, &value); err != nil {
			return nil, err
		}
	} else if len(data) != 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
	}
	return redactSeedValue(value), nil
}

func redactSeedValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = redactSeedValue(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = redactSeedValue(child)
		}
		return result
	case string:
		return "<redacted:string>"
	case bool:
		return "<redacted:bool>"
	case int64, int, float64:
		return "<redacted:number>"
	case nil:
		return "<redacted:null>"
	default:
		return "<redacted>"
	}
}

func hookArtifactRecords(plans []hooks.TargetPlan) ([]environment.ArtifactRecord, error) {
	var records []environment.ArtifactRecord
	for _, plan := range plans {
		current, root := plan.Target, plan.TargetRoot
		seen := make(map[string]int)
		for _, file := range plan.Files {
			relative, err := filepath.Rel(root, file.Path)
			if err != nil || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("planned hook output escapes %s: %s", root, file.Path)
			}
			relative = filepath.ToSlash(relative)
			record := environment.ArtifactRecord{
				ID: "hook-file:" + string(current) + ":" + relative, Kind: "hook_file", Name: filepath.Base(relative), Module: file.Module, Winner: "hook", OutputPath: relative, Evidence: environment.EvidenceGenerated,
				Properties: map[string]any{"target": string(current), "source_path": file.Source, "rendered_digest": digestBytes(file.Contents)},
			}
			if prior, found := seen[relative]; found {
				record.Winner = "hook"
				record.Properties["overrides"] = records[prior].ID
				records[prior].Omitted = true
				records[prior].Winner = record.ID
				records[prior].Reason = "overridden by later hook output"
			}
			seen[relative] = len(records)
			records = append(records, record)
		}
		for _, hook := range plan.Hooks {
			id := fmt.Sprintf("hook:%s:%s:%s:%d:%d:%d", current, hook.Origin.Module, hook.Origin.SourceRelative, hook.Origin.EventIndex, hook.Origin.MatcherGroupIndex, hook.Origin.HookIndex)
			records = append(records, environment.ArtifactRecord{ID: id, Kind: "hook", Name: string(hook.Event), Module: hook.Origin.Module, Winner: "hook", Evidence: environment.EvidenceGenerated, Properties: map[string]any{"target": string(current), "source_path": hook.Origin.SourceFile, "handler": string(hook.Handler.Kind)}})
		}
	}
	return records, nil
}

func recordOutputPaths(record environment.ArtifactRecord) []string {
	paths := []string{}
	if record.OutputPath != "" {
		paths = append(paths, record.OutputPath)
	}
	if additional, ok := record.Properties["additional_outputs"].([]string); ok {
		paths = append(paths, additional...)
	}
	return paths
}

func appendArtifactPropertyString(record *environment.ArtifactRecord, key, value string) {
	values, _ := record.Properties[key].([]string)
	if !slices.Contains(values, value) {
		record.Properties[key] = append(values, value)
	}
}
