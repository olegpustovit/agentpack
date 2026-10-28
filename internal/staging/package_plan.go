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
	"github.com/olegpustovit/agentpack/internal/mode"
)

// PackageTreePlan is the parsed package view consumed by staging and preflight.
// Files contain the exact bytes to publish, so planning never rereads a source
// after reporting its generated output.
type PackageTreePlan struct {
	SourceRoot   string
	SourceDigest string
	Artifacts    []PlannedArtifact
	Files        []PlannedPackageFile
	Warnings     []string
}

// PlannedArtifact is one parsed markdown item and its target-specific output.
type PlannedArtifact struct {
	Target            harness.Target
	SourceRelative    string
	VirtualPath       string
	Source            []byte
	Parsed            *artifacts.Markdown
	Name              string
	Kind              artifacts.Kind
	OutputPath        string
	Output            []byte
	AdditionalOutputs []string
	Omitted           bool
	Reason            string
	Warnings          []string
}

// PlannedPackageFile is one generated artifact or copied plugin support file.
type PlannedPackageFile struct {
	Target         harness.Target
	SourceRelative string
	RelativePath   string
	Contents       []byte
	ExecuteBits    os.FileMode
}

// PlanPackageTree reads a package once, applies mode filtering and target
// rendering, and returns the files that staging would publish. Plugin support
// files are included when plugin is true.
func PlanPackageTree(sourceRoot, module, bareSkillName string, effective mode.Effective, roots []HarnessRoot, plugin bool) (PackageTreePlan, error) {
	digest, err := cache.TreeDigest(sourceRoot)
	if err != nil {
		return PackageTreePlan{}, err
	}
	files, err := PackageFiles(sourceRoot, module, plugin)
	if err != nil {
		return PackageTreePlan{}, err
	}
	plan := PackageTreePlan{SourceRoot: sourceRoot, SourceDigest: digest}
	contents := make(map[string][]byte)
	if plugin {
		if err := planRawSupport(sourceRoot, module, effective, roots, &plan, contents); err != nil {
			return PackageTreePlan{}, err
		}
	}
	type parsedFile struct {
		file    PackageFile
		data    []byte
		parsed  *artifacts.Markdown
		allowed bool
	}
	parsed := make([]parsedFile, 0)
	skillSources := make(map[string]string)
	for _, file := range files {
		allowed, err := effective.AllowsPackagePath(module, file.Real)
		if err != nil {
			return PackageTreePlan{}, err
		}
		if destination, ok := artifacts.StagedSkillSupportPath(file.Virtual, bareSkillName); ok {
			if allowed {
				data, err := packageFileContents(file.Path, contents)
				if err != nil {
					return PackageTreePlan{}, err
				}
				info, err := os.Stat(file.Path)
				if err != nil {
					return PackageTreePlan{}, err
				}
				for _, root := range roots {
					plan.Files = append(plan.Files, PlannedPackageFile{Target: root.Target, SourceRelative: file.Real, RelativePath: destination, Contents: data, ExecuteBits: info.Mode().Perm() & 0o111})
				}
			}
			continue
		}
		extension := strings.ToLower(filepath.Ext(file.Virtual))
		if extension != ".md" && extension != ".mdc" {
			continue
		}
		data, err := packageFileContents(file.Path, contents)
		if err != nil {
			return PackageTreePlan{}, err
		}
		artifact, parseErr := artifacts.Parse(file.Virtual, string(data), bareSkillName)
		if parseErr != nil {
			if allowed {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: skipping markdown artifact %s: %v", module, file.Real, parseErr))
			}
			continue
		}
		if artifact == nil {
			continue
		}
		parsed = append(parsed, parsedFile{file: file, data: data, parsed: artifact, allowed: allowed})
		if artifact.Kind == artifacts.Skill && allowed {
			if first, duplicate := skillSources[artifact.StorageName]; duplicate && first != file.Real {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: skill %s is published twice (%s and %s); the second is staged", module, artifact.StorageName, first, file.Real))
			}
			skillSources[artifact.StorageName] = file.Real
		}
	}
	converted := NewConvertedNames(skillSources)
	flattened := make(map[harness.Target]map[string]string)
	for _, source := range parsed {
		if len(roots) == 0 {
			item := PlannedArtifact{SourceRelative: source.file.Real, VirtualPath: source.file.Virtual, Source: source.data, Parsed: source.parsed, Name: source.parsed.StorageName, Kind: source.parsed.Kind, Omitted: !source.allowed}
			if item.Omitted {
				item.Reason = "disabled by mode"
			}
			plan.Artifacts = append(plan.Artifacts, item)
			continue
		}
		for _, root := range roots {
			item := PlannedArtifact{Target: root.Target, SourceRelative: source.file.Real, VirtualPath: source.file.Virtual, Source: source.data, Parsed: source.parsed, Name: source.parsed.StorageName, Kind: source.parsed.Kind, Omitted: !source.allowed}
			if item.Omitted {
				item.Reason = "disabled by mode"
				plan.Artifacts = append(plan.Artifacts, item)
				continue
			}
			staged := converted.For(source.parsed, root.Target)
			if staged.StorageName != source.parsed.StorageName {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: %s stages %ss as skills and the skill name %s is taken; %s %s is staged as skill %s", module, root.Target, source.parsed.Kind.Word(), source.parsed.StorageName, source.parsed.Kind.Word(), source.parsed.StorageName, staged.StorageName))
			}
			rendered := staged.Render(root.Target)
			if nested := staged.NestedPath(); staged.Kind == artifacts.Agent && rendered.RelativePath != nested {
				if flattened[root.Target] == nil {
					flattened[root.Target] = make(map[string]string)
				}
				if first, taken := flattened[root.Target][rendered.RelativePath]; taken {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: agents %s and %s have the same file name; %s reads agents only at the top of agents/, so it finds the first", module, first, nested, harness.DisplayName(root.Target)))
					rendered.RelativePath = nested
				} else {
					flattened[root.Target][rendered.RelativePath] = nested
				}
			}
			item.Name = staged.StorageName
			item.OutputPath = rendered.RelativePath
			item.Output = []byte(rendered.Contents)
			item.Warnings = rendered.Warnings
			for _, warning := range rendered.Warnings {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: %s", module, warning))
			}
			plan.Files = append(plan.Files, PlannedPackageFile{Target: root.Target, SourceRelative: source.file.Real, RelativePath: rendered.RelativePath, Contents: item.Output})
			if native, ok := source.parsed.CodexAgent(); ok && root.Target == harness.Codex {
				plan.Files = append(plan.Files, PlannedPackageFile{Target: root.Target, SourceRelative: source.file.Real, RelativePath: native.RelativePath, Contents: []byte(native.Contents)})
				item.AdditionalOutputs = append(item.AdditionalOutputs, native.RelativePath)
			}
			plan.Artifacts = append(plan.Artifacts, item)
		}
	}
	return plan, nil
}

// ApplyPackageTreePlan verifies source integrity before writing the planned bytes.
func ApplyPackageTreePlan(plan PackageTreePlan, roots []HarnessRoot) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	rootByTarget := make(map[harness.Target]string, len(roots))
	for _, root := range roots {
		rootByTarget[root.Target] = root.Path
	}
	for _, file := range plan.Files {
		root, ok := rootByTarget[file.Target]
		if !ok {
			continue
		}
		destination := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if err := writeFile(destination, file.Contents); err != nil {
			return err
		}
		if err := os.Chmod(destination, 0o644|file.ExecuteBits); err != nil {
			return fmt.Errorf("set staged permissions %s: %w", destination, err)
		}
	}
	for _, warning := range plan.Warnings {
		log.Printf("warning: %s", warning)
	}
	return nil
}

func planRawSupport(sourceRoot, module string, effective mode.Effective, roots []HarnessRoot, plan *PackageTreePlan, contents map[string][]byte) error {
	destinations := make(map[string][]harness.Target)
	for _, root := range roots {
		for _, subdirectory := range root.Target.RawPluginSubdirs() {
			destinations[subdirectory] = append(destinations[subdirectory], root.Target)
		}
	}
	subdirectories := make([]string, 0, len(destinations))
	for subdirectory := range destinations {
		subdirectories = append(subdirectories, subdirectory)
	}
	sort.Strings(subdirectories)
	for _, subdirectory := range subdirectories {
		source := filepath.Join(sourceRoot, subdirectory)
		if info, err := os.Stat(source); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			fullRelative := filepath.ToSlash(filepath.Join(subdirectory, relative))
			allowed, err := effective.AllowsPackagePath(module, fullRelative)
			if err != nil || !allowed {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			data, err := packageFileContents(path, contents)
			if err != nil {
				return err
			}
			for _, target := range destinations[subdirectory] {
				plan.Files = append(plan.Files, PlannedPackageFile{Target: target, SourceRelative: fullRelative, RelativePath: fullRelative, Contents: data, ExecuteBits: info.Mode().Perm() & 0o111})
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func packageFileContents(path string, contents map[string][]byte) ([]byte, error) {
	if data, ok := contents[path]; ok {
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	contents[path] = data
	return data, nil
}

// Validate also detects newly added files and changed package manifests or links.
func (plan PackageTreePlan) Validate() error {
	actual, err := cache.TreeDigest(plan.SourceRoot)
	if err != nil {
		return fmt.Errorf("package input changed after planning: %s: %w", plan.SourceRoot, err)
	}
	if actual != plan.SourceDigest {
		return fmt.Errorf("package input changed after planning: %s", plan.SourceRoot)
	}
	return nil
}
