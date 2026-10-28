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
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
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
// Codex skills are returned as native discoveries and never as staged files.
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
	includeClaude := target == nil || *target == harness.Claude
	includeCodex := target == nil || *target == harness.Codex
	if !includeClaude && !includeCodex {
		return plan, nil
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
		return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
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
			overlayEnabled := os.Getenv("AGENTPACK_DOT_AGENTS") != "0"
			if allowed && overlayEnabled {
				plan.Files = append(plan.Files, DotAgentsFile{Target: destination, SourcePath: path, SourceRelative: selectorRelative, RelativePath: filepath.ToSlash(destinationRelative), Contents: data, HardLink: true})
			}
			parseRelative := filepath.ToSlash(relative)
			if sourceRelative != "claude" && sourceRelative != "codex" {
				parseRelative = filepath.ToSlash(filepath.Join(sourceRelative, relative))
			}
			addParsedDotArtifact(&plan, destination, selectorRelative, parseRelative, filepath.ToSlash(destinationRelative), data, allowed && overlayEnabled, false)
			return nil
		})
	}
	if includeClaude {
		if err := addRawTree("claude", harness.Claude, true); err != nil {
			return DotAgentsPlan{}, err
		}
	}
	if includeCodex {
		if err := addRawTree("codex", harness.Codex, true); err != nil {
			return DotAgentsPlan{}, err
		}
	}
	if includeClaude {
		if err := planDotRules(dotRoot, effective, &plan); err != nil {
			return DotAgentsPlan{}, err
		}
		if err := addRawTree("skills", harness.Claude, false); err != nil {
			return DotAgentsPlan{}, err
		}
		for _, source := range []string{"agents", "commands"} {
			if err := addRawTree(source, harness.Claude, false); err != nil {
				return DotAgentsPlan{}, err
			}
		}
	}
	if includeCodex {
		if err := planDotCodexSkills(dotRoot, "skills", effective, &plan); err != nil {
			return DotAgentsPlan{}, err
		}
		for _, source := range []string{"agents", "commands"} {
			if err := planDotCodexSkills(dotRoot, source, effective, &plan); err != nil {
				return DotAgentsPlan{}, err
			}
		}
		if err := planDotGuidance(dotRoot, "AGENTS.md", harness.Codex, effective, &plan); err != nil {
			return DotAgentsPlan{}, err
		}
	}
	if includeClaude {
		if err := planDotGuidance(dotRoot, "CLAUDE.md", harness.Claude, effective, &plan); err != nil {
			return DotAgentsPlan{}, err
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

func ApplyDotAgentsPlan(plan DotAgentsPlan, claudeBundle, codexHome string) error {
	for _, file := range plan.Files {
		root := codexHome
		if file.Target == harness.Claude {
			root = claudeBundle
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

func StageDotAgents(definitionRoot, workspaceRoot, modeName string, effective mode.Effective, codex string, target *harness.Target) error {
	if workspaceRoot == "" {
		workspaceRoot = definitionRoot
	}
	plugins, err := paths.StagingPluginsDirForMode(definitionRoot, modeName)
	if err != nil {
		return err
	}
	plan, err := PlanDotAgents(paths.ProjectDotAgentsDir(workspaceRoot), effective, target)
	if err != nil {
		return err
	}
	return ApplyDotAgentsPlan(plan, filepath.Join(plugins, paths.StagedAgentpackBundleName), codex)
}

func planDotRules(dotRoot string, effective mode.Effective, plan *DotAgentsPlan) error {
	rulesRoot := filepath.Join(dotRoot, "rules")
	if info, err := os.Stat(rulesRoot); err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(rulesRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".mdc") {
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
			plan.Files = append(plan.Files, DotAgentsFile{Target: harness.Claude, SourcePath: path, SourceRelative: sourceRelative, RelativePath: output, Contents: data, HardLink: true})
		}
		addParsedDotArtifact(plan, harness.Claude, sourceRelative, sourceRelative, output, data, allowed && os.Getenv("AGENTPACK_DOT_AGENTS") != "0", false)
		return nil
	})
}

func planDotCodexSkills(dotRoot, sourceRelative string, effective mode.Effective, plan *DotAgentsPlan) error {
	sourceRoot := filepath.Join(dotRoot, sourceRelative)
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
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
		if sourceRelative == "skills" && artifact.Kind == artifacts.Skill {
			plan.Artifacts = append(plan.Artifacts, DotAgentsArtifact{Target: harness.Codex, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName, NativeSource: selectorRelative})
			return nil
		}
		if os.Getenv("AGENTPACK_DOT_AGENTS") == "0" {
			plan.Artifacts = append(plan.Artifacts, DotAgentsArtifact{Target: harness.Codex, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName, Omitted: true, Reason: "disabled by AGENTPACK_DOT_AGENTS"})
			return nil
		}
		if !allowed {
			plan.Artifacts = append(plan.Artifacts, DotAgentsArtifact{Target: harness.Codex, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName, Omitted: true, Reason: "disabled by mode or lock"})
			return nil
		}
		if artifact.Kind == artifacts.Skill {
			plan.Artifacts = append(plan.Artifacts, DotAgentsArtifact{Target: harness.Codex, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName, NativeSource: selectorRelative})
			return nil
		}
		rendered := artifact.Render(harness.Codex)
		plan.Files = append(plan.Files, DotAgentsFile{Target: harness.Codex, SourcePath: path, SourceRelative: selectorRelative, RelativePath: rendered.RelativePath, Contents: []byte(rendered.Contents)})
		plan.Artifacts = append(plan.Artifacts, DotAgentsArtifact{Target: harness.Codex, SourceRelative: selectorRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName, OutputPath: rendered.RelativePath, Output: []byte(rendered.Contents)})
		for _, warning := range rendered.Warnings {
			plan.Warnings = append(plan.Warnings, warning)
		}
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

func addParsedDotArtifact(plan *DotAgentsPlan, target harness.Target, sourceRelative, parseRelative, outputPath string, contents []byte, allowed, native bool) {
	artifact, err := artifacts.Parse(parseRelative, string(contents), "")
	if err != nil || artifact == nil {
		return
	}
	item := DotAgentsArtifact{Target: target, SourceRelative: sourceRelative, Source: contents, Parsed: artifact, Kind: artifact.Kind, Name: artifact.StorageName}
	if native {
		item.NativeSource = sourceRelative
	} else if !allowed {
		item.Omitted = true
		item.Reason = "disabled by mode or lock"
	} else {
		item.OutputPath, item.Output = outputPath, contents
	}
	plan.Artifacts = append(plan.Artifacts, item)
}
