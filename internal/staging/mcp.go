package staging

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
)

type MCPCollectionPlan struct {
	Entries  mcp.Entries
	Warnings []MCPCollectionWarning
}

type MCPCollectionWarning struct{ Source, Message string }

func warnMCP(warnings *[]MCPCollectionWarning, source, message string) {
	*warnings = append(*warnings, MCPCollectionWarning{Source: source, Message: message})
}

// CollectMCP applies the canonical precedence: plugins, manifest, then .agents.
func CollectMCP(projectRoot string, lock lockfile.PackLock, projectManifest *manifest.Manifest, effective *mode.Effective) (mcp.Entries, error) {
	plan, err := CollectMCPPlan(projectRoot, lock, projectManifest, effective)
	if err != nil {
		return nil, err
	}
	for _, warning := range plan.Warnings {
		log.Printf("warning: %s: %s", warning.Source, warning.Message)
	}
	return plan.Entries, nil
}

func CollectMCPPlan(projectRoot string, lock lockfile.PackLock, projectManifest *manifest.Manifest, effective *mode.Effective) (MCPCollectionPlan, error) {
	merged := make(mcp.Entries)
	var warnings []MCPCollectionWarning
	plugins := lock.Plugins()
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].CacheKey < plugins[j].CacheKey })
	for _, plugin := range plugins {
		if plugin.CacheKey == "" || disabledPlugin(lock, plugin.CacheKey) {
			continue
		}
		if effective != nil {
			allowed, err := effective.AllowsPackagePath(plugin.Module, "mcp.json")
			if err != nil {
				return MCPCollectionPlan{}, err
			}
			if !allowed {
				continue
			}
		}
		root, err := cache.EntryDir(plugin.CacheKey)
		if err != nil {
			continue
		}
		mergePluginMCP(root, plugin, effective, merged, &warnings)
	}
	if projectManifest != nil {
		for name, server := range projectManifest.MCP.Servers {
			if effective == nil || effective.AllowsMCP(name) {
				merged[name] = mcp.Entry{Server: server, Source: mcp.Manifest}
			}
		}
	}
	dotPath := filepath.Join(paths.ProjectDotAgentsDir(projectRoot), "mcp.json")
	if effective != nil {
		allowed, err := effective.AllowsDotAgentsPath("mcp.json")
		if err != nil {
			return MCPCollectionPlan{}, err
		}
		if !allowed {
			return MCPCollectionPlan{Entries: merged, Warnings: warnings}, nil
		}
	}
	mergeMCPFile(dotPath, mcp.Entry{Source: mcp.DotAgents}, ".agents/mcp.json", effective, merged, &warnings)
	return MCPCollectionPlan{Entries: merged, Warnings: warnings}, nil
}

func disabledPlugin(lock lockfile.PackLock, key string) bool {
	short := key
	if len(short) > 16 {
		short = short[:16]
	}
	for _, disabled := range lock.Config.DisabledPlugins {
		if disabled == key || disabled == short {
			return true
		}
	}
	return false
}

// mergeMCPFile reads one MCP definition file, in the wrapped or the bare form.
// A file or a server that cannot be read is skipped with a warning naming it.
func mergeMCPFile(path string, origin mcp.Entry, label string, effective *mode.Effective, merged mcp.Entries, warnings *[]MCPCollectionWarning) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		warnMCP(warnings, label, "unreadable MCP definition")
		return
	}
	servers, parseWarnings, err := mcp.ParseServers(data)
	if err != nil {
		warnMCP(warnings, label, "invalid MCP definition")
		return
	}
	for _, warning := range parseWarnings {
		warnMCP(warnings, label, "invalid server entry: "+warning)
	}
	mergeServers(servers, origin, effective, merged)
}

func mergeServers(servers map[string]mcp.Server, origin mcp.Entry, effective *mode.Effective, merged mcp.Entries) {
	for name, server := range servers {
		if effective == nil || effective.AllowsMCP(name) {
			origin.Server = server
			merged[name] = origin
		}
	}
}

// mergePluginMCP reads every place a plugin may define MCP servers, as the
// vendors' plugin formats give them: the root mcp.json (Cursor) and .mcp.json
// (Claude Code, Codex), then what each plugin.json declares under mcpServers,
// as a path or inline. Codex is read first, then Cursor, then Claude Code; a
// later definition of the same server name wins.
func mergePluginMCP(root string, plugin lockfile.Package, effective *mode.Effective, merged mcp.Entries, warnings *[]MCPCollectionWarning) {
	origin := mcp.Entry{Source: mcp.Plugin, Module: plugin.Module, CacheKey: plugin.CacheKey}
	read := make(map[string]bool)
	file := func(path, relative string) {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || read[resolved] {
			return
		}
		read[resolved] = true
		mergeMCPFile(resolved, origin, plugin.Module+" "+relative, effective, merged, warnings)
	}
	// The cache keeps a copy of .mcp.json as mcp.json; reading the original
	// last lets the Claude Code and Codex file win where a package has both.
	file(filepath.Join(root, "mcp.json"), "mcp.json")
	file(filepath.Join(root, ".mcp.json"), ".mcp.json")
	for _, pluginManifest := range cache.ReadPluginManifests(root) {
		label := plugin.Module + " " + pluginManifest.Relative + " mcpServers"
		declared, inline := pluginManifest.Declared("mcpServers")
		for _, path := range declared {
			if strings.Contains(path, "://") || !strings.EqualFold(filepath.Ext(path), ".json") {
				warnMCP(warnings, label, "unsupported MCP definition source; only in-package .json files are read")
				continue
			}
			resolved, err := cache.ResolveInside(root, path)
			if err != nil {
				warnMCP(warnings, label, "MCP definition path escapes package or cannot be resolved")
				continue
			}
			file(resolved, strings.TrimPrefix(path, "./"))
		}
		for _, value := range inline {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(value, &raw); err != nil {
				warnMCP(warnings, label, "inline MCP definition is not an object")
				continue
			}
			servers, parseWarnings := mcp.DecodeServers(raw)
			for _, warning := range parseWarnings {
				warnMCP(warnings, label, "invalid server entry: "+warning)
			}
			mergeServers(servers, origin, effective, merged)
		}
	}
}
