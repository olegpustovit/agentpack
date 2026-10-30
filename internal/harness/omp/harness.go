package omp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func New() base.Harness {
	return base.Definition{Target: base.OMP, Root: stagedRoot, Setup: prepare, ProjectMCP: planMCP, ProjectLSP: planLSP, LSP: writeLSP, Guidance: injectGuidance, Check: verify, Launch: launch}
}

func stagedRoot(ctx base.StageContext) (string, error) {
	root, err := paths.StagingRootForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "omp"), nil
}

func prepare(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	for _, directory := range []string{".claude-plugin", ".omp-plugin"} {
		manifest := filepath.Join(root, directory, "plugin.json")
		if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(manifest, []byte(`{"name":"agentpack-bundle"}`), 0o644); err != nil {
			return err
		}
	}
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
		return nil
	}
	return injectGuidance("", ctx)
}

func injectGuidance(blob string, ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "rules", "agentpack-guidance.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("---\nalwaysApply: true\n---\n"), 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
	default:
		blob = base.NoAttributionBody + "\n" + blob
	}
	return base.WriteGuidance(path, blob)
}

func verify(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	for _, name := range []string{".claude-plugin/plugin.json", ".omp-plugin/plugin.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("OMP bundle missing %s: %w", name, err)
		}
	}
	return nil
}

func launch(ctx base.LaunchContext) (*exec.Cmd, error) {
	root, err := stagedRoot(base.StageContext{ProjectRoot: ctx.ProjectRoot, Mode: ctx.Mode})
	if err != nil {
		return nil, err
	}
	binary, err := base.ResolveBinary("OMP_PATH", "omp")
	if err != nil {
		return nil, err
	}
	args := []string{"--plugin-dir", root}
	if ctx.Yolo {
		args = append(args, "--yolo")
	}
	args = append(args, ctx.Arguments...)
	return exec.Command(binary, args...), nil
}
