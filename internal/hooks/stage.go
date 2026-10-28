package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mode"
)

type PlannedFile struct {
	Path     string
	Contents []byte
	Source   string
	Module   string
}

type TargetPlan struct {
	Target      harness.Target
	Files       []PlannedFile
	Diagnostics []Diagnostic
	Summary     RenderSummary
	Skipped     []Diagnostic
	Hooks       []Hook
	Inputs      []OriginInput
}

type OriginInput struct{ Path, Digest string }

// PlanTarget computes the exact support and rendered hook files for a target.
// The caller supplies the same renderer/context that staging will use.
func PlanTarget(bundle Bundle, target harness.Target, renderer Renderer, context RenderContext, effective mode.Effective) (TargetPlan, error) {
	plan := TargetPlan{Target: target, Skipped: slices.Clone(bundle.Skipped)}
	if renderer == nil {
		return plan, nil
	}
	selected := bundle.ForTarget(target)
	if target != harness.Codex {
		selected.Hooks = slices.DeleteFunc(slices.Clone(selected.Hooks), func(hook Hook) bool { return hook.Origin.Layer == SeededNative })
	}
	plan.Hooks = slices.Clone(selected.Hooks)
	seenInputs := make(map[string]bool)
	for _, hook := range selected.Hooks {
		path := hook.Origin.SourceFile
		if path == "" || seenInputs[path] {
			continue
		}
		seenInputs[path] = true
		digest := hook.Origin.SourceDigest
		if digest == "" {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return TargetPlan{}, fmt.Errorf("read hook origin %s: %w", path, err)
			}
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
		}
		plan.Inputs = append(plan.Inputs, OriginInput{Path: path, Digest: digest})
	}
	packageRoots, packageFiles, err := PlanOriginPackages(selected, target, context.TargetRoot, effective)
	if err != nil {
		return TargetPlan{}, err
	}
	context.StagedPackages = packageRoots
	output, err := renderer.Render(selected, context)
	if err != nil {
		return TargetPlan{}, err
	}
	plan.Files = append(plan.Files, packageFiles...)
	outputFiles, err := SerializeRenderedFiles(output)
	if err != nil {
		return TargetPlan{}, err
	}
	for index := range outputFiles {
		outputFiles[index].Source = "hook-renderer"
	}
	plan.Files = append(plan.Files, outputFiles...)
	outputs := make(map[string]bool, len(plan.Files))
	for _, file := range plan.Files {
		outputs[filepath.Clean(file.Path)] = true
	}
	plan.Inputs = slices.DeleteFunc(plan.Inputs, func(input OriginInput) bool { return outputs[filepath.Clean(input.Path)] })
	plan.Diagnostics, plan.Summary = output.Diagnostics, output.Summary
	return plan, nil
}

func ApplyTargetPlan(plan TargetPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	for _, file := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file.Path, file.Contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (plan TargetPlan) Validate() error {
	for _, input := range plan.Inputs {
		data, err := os.ReadFile(input.Path)
		if err != nil {
			return fmt.Errorf("hook origin changed after planning (%s): %w", input.Path, err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != input.Digest {
			return fmt.Errorf("hook origin changed after planning: %s", input.Path)
		}
	}
	return nil
}

func SerializeRenderedFiles(output RenderOutput) ([]PlannedFile, error) {
	files := make([]PlannedFile, 0, len(output.Files))
	for _, file := range output.Files {
		var data []byte
		if file.JSON != nil {
			var err error
			data, err = json.MarshalIndent(file.JSON, "", "  ")
			if err != nil {
				return nil, err
			}
			data = append(data, '\n')
		} else {
			data = []byte(file.Text)
		}
		files = append(files, PlannedFile{Path: file.Path, Contents: data})
	}
	return files, nil
}

func StageOriginPackages(bundle Bundle, target harness.Target, targetRoot string, effective mode.Effective) (map[string]string, error) {
	roots, files, err := PlanOriginPackages(bundle, target, targetRoot, effective)
	if err != nil {
		return nil, err
	}
	plan := TargetPlan{Files: files}
	if err := ApplyTargetPlan(plan); err != nil {
		return nil, err
	}
	return roots, nil
}

func PlanOriginPackages(bundle Bundle, target harness.Target, targetRoot string, effective mode.Effective) (map[string]string, []PlannedFile, error) {
	roots := make(map[string]string)
	var files []PlannedFile
	seen := make(map[string]bool)
	for _, hook := range bundle.Hooks {
		origin := hook.Origin
		if origin.Layer == SeededNative || seen[origin.SourceID()] {
			continue
		}
		seen[origin.SourceID()] = true
		destination := filepath.Join(HookAssetRoot(target, targetRoot), origin.PackageKey, "package")
		planned, err := planFilteredTree(origin.SourceRoot, destination, origin, effective)
		if err != nil {
			return nil, nil, err
		}
		for index := range planned {
			planned[index].Source = origin.SourceFile
			planned[index].Module = origin.Module
		}
		files = append(files, planned...)
		roots[origin.PackageKey] = destination
	}
	return roots, files, nil
}

func planFilteredTree(source, destination string, origin Origin, effective mode.Effective) ([]PlannedFile, error) {
	info, err := os.Stat(source)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	var files []PlannedFile
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		runtimePath := filepath.ToSlash(relative)
		allowed := true
		switch origin.Layer {
		case PackPlugin, BareSkill:
			allowed, err = effective.AllowsPackagePath(origin.Module, runtimePath)
		case DotAgents:
			allowed, err = effective.AllowsDotAgentsPath(runtimePath)
		}
		if err != nil {
			return err
		}
		if !allowed {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		output := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(output, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, PlannedFile{Path: output, Contents: data})
		return nil
	})
	return files, err
}
