package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	library "github.com/olegpustovit/agentpack/internal/registry"
	"github.com/olegpustovit/agentpack/internal/staging"
)

// PreflightOptions selects the pure offline check surface.
type PreflightOptions struct {
	Mode           string
	Target         *base.Target
	Policy         environment.Policy
	StrictExternal bool
	WorkspaceRoot  string
	ContractPath   string // optional; empty loads <definition>/contract.json when present
	ReceiptID      string // optional; compare a prior native probe receipt (read-only)
}

// PreflightInputError marks malformed or inconsistent definition inputs.
// Filesystem failures remain their original errors so callers can keep them
// classified as operational failures.
type PreflightInputError struct{ Err error }

func (err *PreflightInputError) Error() string { return err.Err.Error() }
func (err *PreflightInputError) Unwrap() error { return err.Err }

func inputError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &PreflightInputError{Err: err}
}

// Preflight inspects an already-available definition without repairing cache,
// downloading, amending the lock, rebuilding staging, or writing overlays.
func (service Service) Preflight(definitionRoot string, options PreflightOptions) (environment.Report, error) {
	if options.Policy == "" {
		options.Policy = environment.PolicyLocal
	}
	workspace := options.WorkspaceRoot
	if workspace == "" {
		workspace = definitionRoot
	}
	report := newPreflightReport(definitionRoot, workspace, options)

	home, err := paths.UserAgentpackHome()
	if err != nil {
		return report, err
	}
	pending, err := library.PendingRestore(home, definitionRoot)
	if err != nil {
		return report, fmt.Errorf("check interrupted library restore: %w", err)
	}
	if pending {
		return report, fmt.Errorf("library restore was interrupted; run agentpack sync to recover the definition before preflight")
	}
	project, err := manifest.Load(definitionRoot)
	if err != nil {
		return report, inputError(err)
	}
	file, err := loadLockFile(definitionRoot)
	if err != nil {
		return report, inputError(err)
	}
	lock := file.lock
	effective, err := resolveMode(workspace, project, &lock, options.Mode)
	if err != nil {
		return report, inputError(err)
	}
	report.Mode = effective.Name()
	report.Skills = lock.SkillCount()
	report.Plugins = lock.PluginCount()
	plugins := lock.Plugins()
	for _, skill := range lock.Skills() {
		if staging.SkillIsShadowed(skill, plugins) {
			report.Shadowed++
		}
	}
	in := Inputs{Lock: file.digest}
	if in.Content.Source, err = sourceIdentity(definitionRoot, lock); err != nil {
		return report, fmt.Errorf("identify source: %w", inputError(err))
	}
	report.SourceIdentity, report.LockDigest = in.Content.Source, in.Lock
	if binding, found, _ := environment.LoadBinding(workspace); found {
		report.Environment = binding.Environment
	}
	cacheReady := appendCacheFindings(&report, lock)
	var hookPlans []hooks.TargetPlan
	report.Artifacts, hookPlans, err = plannedArtifacts(lock, effective, definitionRoot, workspace, options.Target, cacheReady)
	if err != nil {
		return report, fmt.Errorf("plan effective artifacts: %w", err)
	}
	report.Skills, report.Plugins = 0, 0
	for _, artifact := range report.Artifacts {
		if !artifact.Omitted {
			if artifact.Kind == "skill" {
				report.Skills++
			}
			if artifact.Kind == "plugin" {
				report.Plugins++
			}
		}
	}
	if err := populateFreshness(&report, in, options, effective); err != nil {
		return report, fmt.Errorf("fingerprint preflight inputs: %w", err)
	}
	appendAmbientFindings(&report, workspace, options)
	appendArtifactFindings(&report)
	appendInheritanceFindings(&report, options)
	appendOverlayFindings(&report, options)
	appendMCPFindings(&report, lock, project)
	appendTargetFindings(&report, options.Target, hookPlans)

	pipeline := staging.Pipeline{
		ProjectRoot:    definitionRoot,
		WorkspaceRoot:  workspace,
		Lock:           lock,
		Manifest:       project,
		Mode:           effective,
		Target:         options.Target,
		StrictExternal: options.StrictExternal,
	}
	if err := appendStagingFindings(&report, pipeline, file.digest); err != nil {
		return report, err
	}

	if options.ReceiptID != "" {
		applyReceipt(&report, workspace, options)
	}

	if options.ContractPath != "" {
		contract, err := environment.LoadContract(options.ContractPath)
		if err != nil {
			return report, fmt.Errorf("load contract: %w", inputError(err))
		}
		applyContract(&report, contract)
	} else if path := filepath.Join(definitionRoot, "contract.json"); paths.IsRegularFile(path) {
		contract, err := environment.LoadContract(path)
		if err != nil {
			return report, fmt.Errorf("load contract: %w", inputError(err))
		}
		applyContract(&report, contract)
	}

	report.OverallStatus = overallStatus(report)
	report.OK = report.OverallStatus == "ready" || report.OverallStatus == "ready_with_warnings"
	return report, nil
}

func newPreflightReport(definitionRoot, workspace string, options PreflightOptions) environment.Report {
	report := environment.Report{
		SchemaVersion:  1,
		Phase:          "preflight",
		DefinitionRoot: definitionRoot,
		WorkspaceRoot:  workspace,
		Policy:         options.Policy,
		StrictExternal: options.StrictExternal,
		Capabilities:   map[string]string{},
		Coverage: []environment.CoverageNote{{
			Category: "skill", Scope: "native_catalog", Completeness: "none",
			Reason: "pure preflight does not execute native CLI discovery",
		}},
	}
	if options.Target != nil {
		report.Target = string(*options.Target)
	}
	for _, target := range base.AllTargets() {
		status, note := environment.ExternalCapability(target)
		report.Capabilities[string(target)] = status + ": " + note
	}
	return report
}

// RestoreFrozenOptions ensures locked cache trees exist and rebuilds staging without
// re-resolving pins or rewriting pack.lock. Missing locked inputs fail hard.
func (service Service) RestoreFrozenOptions(ctx context.Context, definitionRoot string, options SyncOptions) error {
	options.Frozen = true
	options.UpdateLock = false
	options.Repair = false
	options.DryRun = false
	options.VerifyOnly = false
	_, err := service.Sync(ctx, definitionRoot, options)
	return err
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func populateFreshness(report *environment.Report, in Inputs, options PreflightOptions, effective mode.Effective) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	inputs := workspaceConfigInputs(report.WorkspaceRoot, home)
	// Native project skills can change what a harness lists and retire earlier observations.
	for _, candidate := range registry.All() {
		if candidate.ProjectSkillRoot != "" && !candidate.ProjectSkillsShadow {
			inputs = append(inputs, filepath.Join(report.WorkspaceRoot, filepath.FromSlash(candidate.ProjectSkillRoot)))
		}
	}
	inputs = append(inputs, inheritedConfigPaths(options.Target, report.WorkspaceRoot)...)
	report.WorkspaceDigest, err = base.FingerprintFiles(inputs)
	if err != nil {
		return err
	}
	stagedPath, err := paths.StagedManifestPath(report.DefinitionRoot, effective.Name())
	if err != nil {
		return err
	}
	stagedBytes, err := os.ReadFile(stagedPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var stagedConfigs []string
	if options.Target != nil {
		candidate, err := registry.ByTarget(*options.Target)
		if err != nil {
			return err
		}
		ctx := base.StageContext{ProjectRoot: report.DefinitionRoot, WorkspaceRoot: report.WorkspaceRoot, Mode: effective}
		root, err := candidate.Root(ctx)
		if err != nil {
			return err
		}
		for _, name := range []string{"config.toml", "opencode.json", "cli-config.json", "AGENTS.md", "CLAUDE.md", "plugin.json", ".claude-plugin/plugin.json", "hooks.json", ".mcp.json", "mcp.json", "mcp_config.json"} {
			stagedConfigs = append(stagedConfigs, filepath.Join(root, filepath.FromSlash(name)))
		}
		for _, name := range candidate.StagedConfigFiles {
			stagedConfigs = append(stagedConfigs, filepath.Join(root, filepath.FromSlash(name)))
		}
	}
	stagedConfigDigest, err := base.FingerprintFiles(stagedConfigs)
	if err != nil {
		return err
	}
	report.GenerationID = in.generation(effective, report.WorkspaceDigest, stagedConfigDigest, stagedBytes)
	contractPath := options.ContractPath
	if contractPath == "" {
		contractPath = filepath.Join(report.DefinitionRoot, "contract.json")
	}
	contractBytes, err := os.ReadFile(contractPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	report.PolicyDigest = digestBytes(append([]byte(fmt.Sprintf("policy=%s\nstrict=%t\n", options.Policy, options.StrictExternal)), contractBytes...))
	if options.Target != nil {
		report.ExpectedTarget.Adapter = string(*options.Target)
		var capability map[string]string
		if probe := selectedHarness(options.Target).Capability; probe != nil {
			capability = probe()
		}
		report.ExpectedTarget.AdapterRevision = capability["adapter_revision"]
		report.ExpectedTarget.CapabilityRevision = capability["capability_revision"]
		if path, err := base.ResolveBinary(options.Target.Binary()); err == nil {
			identity, err := environment.ExecutableIdentity(path)
			if err == nil {
				report.ExpectedTarget.ExecutableIdentity = identity
			}
		}
	}
	return nil
}

// Select configuration explicitly: a home workspace also contains global runtime state.
func workspaceConfigInputs(workspace, home string) []string {
	inputs := []string{filepath.Join(workspace, ".agents"), filepath.Join(workspace, "AGENTS.md"), filepath.Join(workspace, "CLAUDE.md"), filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".grok", "skills")}
	for _, directory := range []string{".claude", ".cursor"} {
		for _, name := range []string{"settings.json", "settings.local.json", "cli-config.json", "mcp.json", ".mcp.json", "CLAUDE.md", "AGENTS.md", "skills", "commands", "agents", "rules", "hooks", "hooks.json", "plugins/marketplace.json"} {
			inputs = append(inputs, filepath.Join(workspace, directory, filepath.FromSlash(name)))
		}
	}
	return inputs
}

func inheritedConfigSeed(harness base.Harness) (string, string) {
	if harness.Inheritance.Seed == nil {
		return "", ""
	}
	return harness.Inheritance.Seed()
}

// selectedHarness is the harness preflight reports on; the zero Harness when none is selected.
func selectedHarness(target *base.Target) base.Harness {
	if target == nil {
		return base.Harness{}
	}
	entry, _ := registry.ByTarget(*target)
	return entry.Harness
}

func inheritedConfigPaths(target *base.Target, workspace string) []string {
	if paths := selectedHarness(target).Inheritance.Paths; paths != nil {
		return paths(workspace)
	}
	return nil
}
