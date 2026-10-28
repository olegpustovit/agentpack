package staging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/mode"
)

type HarnessRoot struct {
	Target harness.Target
	Path   string
}

func StagePackOverlay(lock lockfile.PackLock, roots []HarnessRoot, effective mode.Effective) ([]PackageTreePlan, error) {
	var plans []PackageTreePlan
	plugins := lock.Plugins()
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].CacheKey < plugins[j].CacheKey })
	for _, plugin := range plugins {
		if plugin.CacheKey == "" || disabledPlugin(lock, plugin.CacheKey) {
			continue
		}
		cacheRoot, err := cache.EntryDir(plugin.CacheKey)
		if err != nil {
			continue
		}
		if !cache.HasPluginManifest(cacheRoot) {
			log.Printf("warning: skip plugin staging: cache missing manifest at %s", cacheRoot)
			continue
		}
		plan, err := PlanPackageTree(cacheRoot, plugin.Module, "", effective, roots, true)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
		if err := ApplyPackageTreePlan(plan, roots); err != nil {
			return nil, err
		}
	}
	skills := lock.Skills()
	sort.Slice(skills, func(i, j int) bool { return skills[i].CacheKey < skills[j].CacheKey })
	for _, skill := range skills {
		if skill.CacheKey == "" || disabledPlugin(lock, skill.CacheKey) || SkillIsShadowed(skill, plugins) {
			continue
		}
		cacheRoot, err := cache.EntryDir(skill.CacheKey)
		if err != nil {
			continue
		}
		if info, err := os.Stat(filepath.Join(cacheRoot, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
			log.Printf("warning: skip skill staging: SKILL.md missing at %s", cacheRoot)
			continue
		}
		plan, err := PlanPackageTree(cacheRoot, skill.Module, SkillFolderName(skill), effective, roots, false)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
		if err := ApplyPackageTreePlan(plan, roots); err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func stageSourceTree(sourceRoot string, roots []HarnessRoot, bareSkillName, module string, effective mode.Effective) error {
	plan, err := PlanPackageTree(sourceRoot, module, bareSkillName, effective, roots, bareSkillName == "")
	if err != nil {
		return err
	}
	return ApplyPackageTreePlan(plan, roots)
}

// ConvertedNames names the skills that a package's commands, agents and rules
// become on a target without those kinds. An item keeps its own name when it
// is free; otherwise it gets the name followed by its kind (review-command),
// then by a number. Items must be offered in staging order.
type ConvertedNames struct {
	skills map[string]string
	taken  map[harness.Target]map[string]bool
}

// NewConvertedNames takes the package's own skills, by storage name.
func NewConvertedNames(skills map[string]string) *ConvertedNames {
	return &ConvertedNames{skills: skills, taken: make(map[harness.Target]map[string]bool)}
}

// For returns the artifact as it is staged for target.
func (names *ConvertedNames) For(artifact *artifacts.Markdown, target harness.Target) artifacts.Markdown {
	if artifact.Kind == artifacts.Skill || artifacts.RenderedKind(target, artifact.Kind) != artifacts.Skill {
		return *artifact
	}
	if names.taken[target] == nil {
		names.taken[target] = make(map[string]bool)
	}
	free := func(name string) bool {
		_, skill := names.skills[name]
		return !skill && !names.taken[target][name]
	}
	name := artifact.StorageName
	if !free(name) {
		name = artifact.StorageName + "-" + artifact.Kind.Word()
		for suffix := 2; !free(name); suffix++ {
			name = fmt.Sprintf("%s-%s-%d", artifact.StorageName, artifact.Kind.Word(), suffix)
		}
	}
	names.taken[target][name] = true
	if name == artifact.StorageName {
		return *artifact
	}
	return artifact.Renamed(name)
}

func SkillFolderName(pkg lockfile.Package) string {
	if pkg.Path == "" {
		return pkg.Repo
	}
	name := filepath.Base(filepath.FromSlash(strings.TrimRight(pkg.Path, "/")))
	if name == "." || name == string(filepath.Separator) {
		return pkg.Repo
	}
	return name
}

func SkillIsShadowed(skill lockfile.Package, plugins []lockfile.Package) bool {
	for _, plugin := range plugins {
		if plugin.Kind != lockfile.PackagePlugin || plugin.CacheKey == "" || plugin.Commit == "" || plugin.Owner == "" || plugin.Repo == "" {
			continue
		}
		if skill.Owner != plugin.Owner || skill.Repo != plugin.Repo || skill.Commit != plugin.Commit {
			continue
		}
		pluginPath, skillPath := strings.TrimRight(plugin.Path, "/"), strings.TrimRight(skill.Path, "/")
		if pluginPath == "" || skillPath == pluginPath || strings.HasPrefix(skillPath, pluginPath+"/") {
			return true
		}
	}
	return false
}

func copyFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", source, err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	if err := writeFile(destination, data); err != nil {
		return err
	}
	if err := os.Chmod(destination, 0o644|info.Mode().Perm()&0o111); err != nil {
		return fmt.Errorf("set staged permissions %s: %w", destination, err)
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
