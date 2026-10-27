package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/staging"
)

// EnvironmentDigest compares selected runtime inputs across a package mutation.
// It excludes lock bookkeeping and generation paths. MCP credentials influence
// the digest, so persist it only in private local state and never publish it.
func EnvironmentDigest(definitionRoot, workspace, selectedMode string, target harness.Target) (string, error) {
	lock, err := loadCheckedLock(definitionRoot)
	if os.IsNotExist(rootCause(err)) {
		lock, err = lockfile.EmptyForProject(definitionRoot), nil
	}
	if err != nil {
		return "", err
	}
	for _, pkg := range lock.Packages {
		if err := cache.VerifyPackage(pkg); err != nil {
			return "", err
		}
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return "", err
	}
	effective, err := resolveMode(workspace, project, &lock, selectedMode)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	file := func(module, name, virtual, path string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return encoder.Encode(struct {
			Module, Name, Path string
			Executable         bool
			Digest             [32]byte
		}{module, name, virtual, info.Mode()&0111 != 0, sha256.Sum256(data)})
	}
	slices.SortFunc(lock.Packages, func(a, b lockfile.Package) int { return strings.Compare(a.Module, b.Module) })
	for _, pkg := range lock.Packages {
		if pkg.Kind == lockfile.PackagePlugin && (slices.Contains(lock.Config.DisabledPlugins, pkg.CacheKey) || len(pkg.CacheKey) >= 16 && slices.Contains(lock.Config.DisabledPlugins, pkg.CacheKey[:16])) {
			continue
		}
		root, err := cache.EntryDir(pkg.CacheKey)
		if err != nil {
			return "", err
		}
		files, err := staging.PackageFiles(root, pkg.Module, pkg.Kind == lockfile.PackagePlugin)
		if err != nil {
			return "", err
		}
		// These definitions are represented by their filtered effective values below.
		definitions := map[string]bool{"mcp.json": true, ".mcp.json": true}
		for _, plugin := range cache.ReadPluginManifests(root) {
			definitions[plugin.Relative] = true
			declared, _ := plugin.Declared("mcpServers")
			for _, path := range declared {
				resolved, err := cache.ResolveInside(root, path)
				if err != nil {
					continue
				} // CollectMCP reports invalid declarations using staging's policy.
				relative, err := filepath.Rel(root, resolved)
				if err != nil {
					return "", err
				}
				definitions[filepath.ToSlash(relative)] = true
			}
		}
		for _, input := range files {
			allowed, err := effective.AllowsPackagePath(pkg.Module, input.Real)
			if err != nil {
				return "", err
			}
			if !allowed || definitions[input.Real] {
				continue
			}
			if err := file(pkg.Module, pkg.Name, input.Virtual, input.Path); err != nil {
				return "", err
			}
		}
	}
	dot := paths.ProjectDotAgentsDir(workspace)
	err = filepath.WalkDir(dot, func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == dot {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(dot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "plugins/"+paths.StagedAgentpackBundleName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || relative == "mcp.json" {
			return nil
		}
		allowed, err := effective.AllowsDotAgentsPath(relative)
		if err != nil || !allowed {
			return err
		}
		return file(".agents", "", relative, path)
	})
	if err != nil {
		return "", err
	}
	bundle, err := hooks.Collect(workspace, lock, "", effective)
	if err != nil {
		return "", err
	}
	bundle = bundle.ForTarget(target)
	for _, hook := range bundle.Hooks {
		hook.Origin.CacheKey, hook.Origin.SourceRoot, hook.Origin.SourceFile, hook.Origin.PackageKey = "", "", "", ""
		if err := encoder.Encode(hook); err != nil {
			return "", err
		}
	}
	entries, err := staging.CollectMCP(workspace, lock, project, &effective)
	if err != nil {
		return "", err
	}
	pinned := mcp.ApplyPins(entries, lock.MCPServers)
	for _, name := range entries.Names() {
		server := pinned[name].Server
		if err := encoder.Encode(struct {
			Name   string
			Server mcp.Server
			OAuth  *mcp.OAuth
		}{name, server, server.OAuth}); err != nil {
			return "", err
		}
		for _, record := range lock.MCPServers {
			if record.Name != name || record.Definition != entries[name].Server.Definition() || !mcp.StartsWithNPX(entries[name].Server) {
				continue
			}
			if spec, ok := mcp.SpecFor(record); ok {
				if mcp.TreeDigest(spec.Tree) != spec.TreeDigest {
					return "", fmt.Errorf("MCP server %q has an invalid locked tree digest", name)
				}
				if err := encoder.Encode(spec); err != nil {
					return "", err
				}
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
