package staging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/mode"
)

type DotAgentsPlan struct {
	SourceRoot   string
	SourceDigest string
	Files        []DotAgentsFile
	Artifacts    []DotAgentsArtifact
	Warnings     []string
}

type DotAgentsFile struct {
	Target         harness.Target
	SourcePath     string
	SourceRelative string
	RelativePath   string
	Contents       []byte
	HardLink       bool
}

type DotAgentsArtifact struct {
	Target         harness.Target
	SourceRelative string
	Source         []byte
	Parsed         *artifacts.Markdown
	Kind           artifacts.Kind
	Name           string
	OutputPath     string
	Output         []byte
	NativeSource   string
	Omitted        bool
	Reason         string
}

// PlanDotAgents computes every dot-agents overlay write in staging order.
// Skills in conversion trees are returned as native discoveries.
func PlanDotAgents(dotRoot string, effective mode.Effective, target *harness.Target) (DotAgentsPlan, error) {
	plan := DotAgentsPlan{SourceRoot: dotRoot}
	if info, err := os.Stat(dotRoot); err == nil && info.IsDir() {
		var err error
		plan.SourceDigest, err = cache.TreeDigest(dotRoot)
		if err != nil {
			return DotAgentsPlan{}, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return DotAgentsPlan{}, err
	}
	info, err := os.Stat(dotRoot)
	if err != nil || !info.IsDir() {
		return plan, nil
	}
	addRawTree := func(sourceRelative string, destination harness.Target, stripPrefix bool) error {
		sourceRoot := filepath.Join(dotRoot, sourceRelative)
		if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
			return nil
		}
		sourceRoot = resolvedDir(sourceRoot)
		return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || skipUnreadableLink(path, entry, &plan) {
				return nil
			}
			relative, err := filepath.Rel(sourceRoot, path)
			if err != nil {
				return err
			}
			selectorRelative := filepath.ToSlash(filepath.Join(sourceRelative, relative))
			allowed, err := effective.AllowsDotAgentsPath(selectorRelative)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			destinationRelative := relative
			if !stripPrefix {
				destinationRelative = filepath.Join(sourceRelative, relative)
			}
			destinationRelative = filepath.ToSlash(destinationRelative)
			overlayEnabled := os.Getenv("AGENTPACK_DOT_AGENTS") != "0"
			if allowed && overlayEnabled {
				plan.Files = append(plan.Files, DotAgentsFile{Target: destination, SourcePath: path, SourceRelative: selectorRelative, RelativePath: destinationRelative, Contents: data, HardLink: true})
			}
			addParsedDotArtifact(&plan, destination, selectorRelative, destinationRelative, destinationRelative, data, allowed && overlayEnabled)
			return nil
		})
	}
	var selected []registry.Entry
	for _, candidate := range registry.All() {
		if candidate.DotAgents != nil && (target == nil || candidate.Target == *target) {
			selected = append(selected, candidate)
		}
	}
	// Native subtrees are overlaid before common trees so common files can replace them.
	for _, candidate := range selected {
		if name := candidate.DotAgents.NativeDirectory; name != "" {
			if err := addRawTree(name, candidate.Target, true); err != nil {
				return DotAgentsPlan{}, err
			}
		}
	}
	for _, candidate := range selected {
		layout := candidate.DotAgents
		if layout.FlattenRules {
			if err := planDotRules(dotRoot, candidate.Target, effective, &plan); err != nil {
				return DotAgentsPlan{}, err
			}
		}
		for _, source := range layout.RawTrees {
			if err := addRawTree(source, candidate.Target, false); err != nil {
				return DotAgentsPlan{}, err
			}
		}
		for _, source := range layout.ConvertedTrees {
			if err := planDotConverted(dotRoot, source, candidate.Target, candidate.Artifacts, effective, &plan); err != nil {
				return DotAgentsPlan{}, err
			}
		}
	}
	for _, candidate := range selected {
		if name := candidate.DotAgents.Guidance; name != "" {
			if err := planDotGuidance(dotRoot, name, candidate.Target, effective, &plan); err != nil {
				return DotAgentsPlan{}, err
			}
		}
	}
	return plan, nil
}

// Validate ensures workspace inputs still match the plan before its staging
// generation is published.
func (plan DotAgentsPlan) Validate() error {
	if plan.SourceDigest == "" {
		return nil
	}
	actual, err := cache.TreeDigest(plan.SourceRoot)
	if err != nil {
		return fmt.Errorf("dot-agents input changed after planning: %s: %w", plan.SourceRoot, err)
	}
	if actual != plan.SourceDigest {
		return fmt.Errorf("dot-agents input changed after planning: %s", plan.SourceRoot)
	}
	return nil
}

func ApplyDotAgentsPlan(plan DotAgentsPlan, roots []HarnessRoot) error {
	rootByTarget := make(map[harness.Target]string, len(roots))
	for _, root := range roots {
		rootByTarget[root.Target] = root.Path
	}
	for _, file := range plan.Files {
		root, ok := rootByTarget[file.Target]
		if !ok {
			return fmt.Errorf("missing %s root for dot-agents overlay", file.Target)
		}
		destination := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if file.HardLink {
			if err := linkOrCopy(file.SourcePath, destination); err != nil {
				return err
			}
		} else if err := writeFile(destination, file.Contents); err != nil {
			return err
		}
	}
	for _, warning := range plan.Warnings {
		log.Printf("warning: %s", warning)
	}
	return nil
}

func linkOrCopy(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Link(source, destination); err == nil {
		return nil
	}
	return copyFile(source, destination)
}

func planDotRules(dotRoot string, target harness.Target, effective mode.Effective, plan *DotAgentsPlan) error {
	rulesRoot := filepath.Join(dotRoot, "rules")
	if info, err := os.Stat(rulesRoot); err != nil || !info.IsDir() {
		return nil
	}
	rulesRoot = resolvedDir(rulesRoot)
	return filepath.WalkDir(rulesRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || skipUnreadableLink(path, entry, plan) || !strings.EqualFold(filepath.Ext(path), ".mdc") {
			return nil
		}
		relative, err := filepath.Rel(rulesRoot, path)
		if err != nil {
			return err
		}
		sourceRelative := filepath.ToSlash(filepath.Join("rules", relative))
		allowed, err := effective.AllowsDotAgentsPath(sourceRelative)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		flattened := strings.ReplaceAll(filepath.ToSlash(relative), "/", "--")
		output := "rules/dot-agents--" + flattened
		if allowed && os.Getenv("AGENTPACK_DOT_AGENTS") != "0" {
			plan.Files = append(plan.Files, DotAgentsFile{Target: target, SourcePath: path, SourceRelative: sourceRelative, RelativePath: output, Contents: data, HardLink: true})
		}
		addParsedDotArtifact(plan, target, sourceRelative, sourceRelative, output, data, allowed && os.Getenv("AGENTPACK_DOT_AGENTS") != "0")
		return nil
	})
}

func planDotConverted(dotRoot, sourceRelative string, target harness.Target, layout *artifacts.Layout, effective mode.Effective, plan *DotAgentsPlan) error {
	sourceRoot := filepath.Join(dotRoot, sourceRelative)
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		return nil
	}
	sourceRoot = resolvedDir(sourceRoot)
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || skipUnreadableLink(path, entry, plan) {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".md" && extension != ".mdc" {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		selectorRelative := filepath.ToSlash(filepath.Join(sourceRelative, relative))
		allowed, err := effective.AllowsDotAgentsPath(selectorRelative)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		artifact, err := artifacts.Parse(selectorRelative, string(contents), "")
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skipping dot-agents artifact %s: %v", selectorRelative, err))
			return nil
		}
		if artifact == nil {
			return nil
		}
		item := DotAgentsArtifact{Target: target, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName}
		switch {
		case artifact.Kind == artifacts.Skill:
			item.NativeSource = selectorRelative
		case os.Getenv("AGENTPACK_DOT_AGENTS") == "0":
			item.Omitted, item.Reason = true, "disabled by AGENTPACK_DOT_AGENTS"
		case !allowed:
			item.Omitted, item.Reason = true, "disabled by mode or lock"
		default:
			rendered := artifact.Render(layout)
			item.OutputPath, item.Output = rendered.RelativePath, []byte(rendered.Contents)
			plan.Files = append(plan.Files, DotAgentsFile{Target: target, SourcePath: path, SourceRelative: selectorRelative, RelativePath: item.OutputPath, Contents: item.Output})
			plan.Warnings = append(plan.Warnings, rendered.Warnings...)
		}
		plan.Artifacts = append(plan.Artifacts, item)
		return nil
	})
}

func planDotGuidance(dotRoot, name string, target harness.Target, effective mode.Effective, plan *DotAgentsPlan) error {
	path := filepath.Join(dotRoot, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	allowed, err := effective.AllowsDotAgentsPath(name)
	if err != nil {
		return err
	}
	if allowed && os.Getenv("AGENTPACK_DOT_AGENTS") != "0" {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		plan.Files = append(plan.Files, DotAgentsFile{Target: target, SourcePath: path, SourceRelative: name, RelativePath: name, Contents: data, HardLink: true})
	}
	return nil
}

func addParsedDotArtifact(plan *DotAgentsPlan, target harness.Target, sourceRelative, parseRelative, outputPath string, contents []byte, allowed bool) {
	artifact, err := artifacts.Parse(parseRelative, string(contents), "")
	if err != nil || artifact == nil {
		return
	}
	item := DotAgentsArtifact{Target: target, SourceRelative: sourceRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName}
	if !allowed {
		item.Omitted = true
		item.Reason = "disabled by mode or lock"
	} else {
		item.OutputPath, item.Output = outputPath, contents
	}
	plan.Artifacts = append(plan.Artifacts, item)
}

// resolvedDir follows a symlinked root (e.g. .agents/skills -> ../.claude/skills);
// WalkDir does not, and would hand the link itself to the callback as a file.
func resolvedDir(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// skipUnreadableLink skips, with a warning, a symlink that is dangling or does
// not point at a regular file; WalkDir would otherwise hand it to ReadFile.
func skipUnreadableLink(path string, entry os.DirEntry, plan *DotAgentsPlan) bool {
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		return false
	}
	if err == nil {
		err = fmt.Errorf("not a regular file")
	}
	plan.Warnings = append(plan.Warnings, fmt.Sprintf("skipping dot-agents link %s: %v", path, err))
	return true
}
