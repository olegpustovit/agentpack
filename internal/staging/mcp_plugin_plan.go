package staging

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/slug"
)

type PlannedMCPPluginFile struct {
	Path       string
	Contents   []byte
	Mode       os.FileMode
	LinkTarget string
	Module     string
}

type MCPPluginSource struct{ Root, Digest, Module string }

type MCPPluginPlan struct {
	Entries     mcp.Entries
	Files       []PlannedMCPPluginFile
	Roots       []string
	DataDirs    []string
	Directories []string
	Sources     []MCPPluginSource
}

func PlanMCPPluginRoots(projectRoot, modeName string, effective mode.Effective, entries mcp.Entries) (MCPPluginPlan, error) {
	roots, err := paths.StagingPluginRootsDirForMode(projectRoot, modeName)
	if err != nil {
		return MCPPluginPlan{}, err
	}
	plan := MCPPluginPlan{Entries: make(mcp.Entries, len(entries))}
	staged := make(map[string]string)
	for _, name := range entries.Names() {
		entry := entries[name]
		plan.Entries[name] = entry
		entry = withPluginCwd(entry)
		if entry.CacheKey == "" || !referencesAny(entry.Server, append(pluginRootVariables, pluginDataVariables...)) {
			continue
		}
		replacements := make(map[string]string)
		if referencesAny(entry.Server, pluginRootVariables) {
			root, found := staged[entry.CacheKey]
			if !found {
				root = filepath.Join(roots, pluginRootName(entry.CacheKey))
				source, err := cache.EntryDir(entry.CacheKey)
				if err != nil {
					return MCPPluginPlan{}, err
				}
				if err := plan.copyPluginRoot(source, root, entry.Module, effective); err != nil {
					return MCPPluginPlan{}, fmt.Errorf("stage plugin directory of %s for MCP server %s: %w", entry.Module, name, err)
				}
				staged[entry.CacheKey] = root
			}
			for _, variable := range pluginRootVariables {
				replacements[variable] = root
			}
		}
		if referencesAny(entry.Server, pluginDataVariables) {
			data, err := paths.PluginDataDir(projectRoot, slug.DashedLower(entry.Module))
			if err != nil {
				return MCPPluginPlan{}, err
			}
			plan.DataDirs = append(plan.DataDirs, data)
			for _, variable := range pluginDataVariables {
				replacements[variable] = data
			}
		}
		entry.Server = substituteServer(entry.Server, replacements)
		plan.Entries[name] = entry
	}
	return plan, nil
}

func (plan *MCPPluginPlan) copyPluginRoot(source, destination, module string, effective mode.Effective) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	digest, err := cache.TreeDigest(resolved)
	if err != nil {
		return err
	}
	plan.Roots = append(plan.Roots, destination)
	plan.Sources = append(plan.Sources, MCPPluginSource{Root: resolved, Digest: digest, Module: module})
	return filepath.WalkDir(resolved, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		output := filepath.Join(destination, relative)
		if entry.IsDir() {
			plan.Directories = append(plan.Directories, output)
			return nil
		}
		allowed, err := effective.AllowsPackagePath(module, filepath.ToSlash(relative))
		if err != nil || !allowed {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolvedTarget, err := filepath.EvalSymlinks(path)
			if err != nil || !cache.Inside(resolved, resolvedTarget) {
				return nil
			}
			plan.Files = append(plan.Files, PlannedMCPPluginFile{Path: output, LinkTarget: target, Module: module})
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		plan.Files = append(plan.Files, PlannedMCPPluginFile{Path: output, Contents: data, Mode: info.Mode().Perm(), Module: module})
		return nil
	})
}

func (plan MCPPluginPlan) Validate() error {
	for _, source := range plan.Sources {
		actual, err := cache.TreeDigest(source.Root)
		if err != nil {
			return fmt.Errorf("MCP plugin input changed after planning: %s: %w", source.Root, err)
		}
		if actual != source.Digest {
			return fmt.Errorf("MCP plugin input changed after planning: %s", source.Root)
		}
	}
	return nil
}

func ApplyMCPPluginPlan(plan MCPPluginPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	for _, root := range plan.Roots {
		if err := os.RemoveAll(root); err != nil {
			return err
		}
	}
	for _, directory := range plan.DataDirs {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	for _, directory := range plan.Directories {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	for _, file := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if file.LinkTarget != "" {
			if err := os.Symlink(file.LinkTarget, file.Path); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(file.Path, file.Contents, file.Mode); err != nil {
			return err
		}
		if err := os.Chmod(file.Path, file.Mode); err != nil {
			return err
		}
	}
	return nil
}
