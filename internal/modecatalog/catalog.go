package modecatalog

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/staging"
)

// CapabilityCatalog knows what mode selectors may name. Paths are checked
// with one Lstat when a selector names them, never by walking every file.
type CapabilityCatalog struct {
	PackageModules map[string]struct{}
	// PackageRoots holds the cache entry of each locked module that has one.
	PackageRoots map[string]string
	// Workspace is where staging reads .agents from.
	Workspace string
	MCPNames  map[string]struct{}
}

// BuildCapabilityCatalog describes what lock, project and the workspace's
// .agents directory offer to selectors; staging reads the same workspace.
func BuildCapabilityCatalog(workspace string, lock *lockfile.PackLock, project *manifest.Manifest) (CapabilityCatalog, error) {
	catalog := CapabilityCatalog{PackageModules: map[string]struct{}{}, PackageRoots: map[string]string{}, Workspace: workspace, MCPNames: map[string]struct{}{}}
	if lock != nil {
		for _, pkg := range lock.Packages {
			if pkg.Module == "" {
				continue
			}
			catalog.PackageModules[pkg.Module] = struct{}{}
			if pkg.CacheKey == "" {
				continue
			}
			root, err := cache.EntryDir(pkg.CacheKey)
			if err != nil {
				return catalog, err
			}
			catalog.PackageRoots[pkg.Module] = root
		}
	}
	if project != nil {
		for module := range project.Dependencies {
			catalog.PackageModules[module] = struct{}{}
		}
	}
	if lock != nil {
		entries, err := staging.CollectMCP(workspace, *lock, project, nil)
		if err != nil {
			return catalog, err
		}
		for name := range entries {
			catalog.MCPNames[name] = struct{}{}
		}
	} else if project != nil {
		for name := range project.MCP.Servers {
			catalog.MCPNames[name] = struct{}{}
		}
	}
	return catalog, nil
}

func (catalog CapabilityCatalog) Validate(selector mode.Selector) error {
	switch selector.Kind {
	case mode.SelectorPackage:
		if _, ok := catalog.PackageModules[selector.Module]; !ok {
			return fmt.Errorf("mode: unknown package selector target: %s", selector.Module)
		}
	case mode.SelectorPackagePath:
		if _, ok := catalog.PackageModules[selector.Module]; !ok {
			return fmt.Errorf("mode: unknown package selector target: %s", selector.Module)
		}
		if root, ok := catalog.PackageRoots[selector.Module]; !ok || !fileExists(root, selector.RelativePath) {
			return fmt.Errorf("mode: unknown package path selector target: %s:%s", selector.Module, selector.RelativePath)
		}
	case mode.SelectorMCP:
		if _, ok := catalog.MCPNames[selector.MCPName]; !ok {
			return fmt.Errorf("mode: unknown MCP selector target: %s", selector.MCPName)
		}
	case mode.SelectorDotAgents:
		if !fileExists(filepath.Join(catalog.Workspace, ".agents"), selector.RelativePath) {
			return fmt.Errorf("mode: unknown .agents selector target: %s", selector.RelativePath)
		}
	}
	return nil
}

func (catalog CapabilityCatalog) ValidateSelector(selector mode.Selector) error {
	return catalog.Validate(selector)
}

// fileExists reports whether relative, a normalized selector path, names a
// file or link (not a directory) under root.
func fileExists(root, relative string) bool {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	return err == nil && !info.IsDir()
}
