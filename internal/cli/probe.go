package cli

import (
	"cmp"
	"context"
	"fmt"

	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/staging"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func (runner Runner) runProbe(ctx context.Context, definitionRoot, workspaceRoot string, invocation Invocation) (int, error) {
	mode, quiet, strict, jsonOut := invocation.Global.Mode, invocation.Global.Quiet, invocation.Global.StrictExternal, boolFlag(invocation, "json")
	policy, err := environment.ParsePolicy(stringFlag(invocation, "policy"))
	if err != nil {
		return 2, err
	}
	agent := cmp.Or(stringFlag(invocation, "agent"), stringFlag(invocation, "target"))
	if agent == "" {
		return 2, fmt.Errorf("probe requires --agent claude|codex|opencode")
	}
	if err := noArgs(invocation.Args); err != nil {
		return 2, err
	}
	target, err := base.ParseTarget(agent)
	if err != nil {
		return 2, err
	}
	if agent == "agent" {
		target = base.Cursor
	}
	entry, err := registry.ByTarget(target)
	if err != nil {
		return 2, err
	}
	if entry.Probe == nil {
		return 2, fmt.Errorf("probe currently supports claude, codex and opencode only (got %s)", target)
	}

	report, err := runner.Service.Preflight(definitionRoot, packSync.PreflightOptions{
		Mode: mode, Target: &target, Policy: policy, WorkspaceRoot: workspaceRoot, StrictExternal: strict,
	})
	if err != nil {
		return 5, err
	}
	if mode == "" {
		mode = report.Mode
	}

	lease, err := staging.AcquireLaunchShared(definitionRoot, mode)
	if err != nil {
		return 5, err
	}
	defer lease.Unlock()
	report, err = runner.Service.Preflight(definitionRoot, packSync.PreflightOptions{Mode: mode, Target: &target, Policy: policy, WorkspaceRoot: workspaceRoot, StrictExternal: strict})
	if err != nil {
		return 5, err
	}
	for _, finding := range report.Findings {
		if finding.Severity == environment.SeverityError || (finding.Severity == environment.SeverityViolation && finding.Code != "CONTRACT_MISMATCH" && finding.Code != "OBSERVATION_UNAVAILABLE") || finding.Code == "STAGING_NOT_RECORDED" || finding.Code == "STAGING_INPUTS_UNKNOWN" {
			if jsonOut {
				if err := writeJSON(runner.Stdout, report); err != nil {
					return 5, err
				}
			}
			return 5, fmt.Errorf("probe cannot observe current staging: %s; restore/sync and resolve preflight findings first", finding.Code)
		}
	}
	var plan base.PackagedNames
	for _, artifact := range report.Artifacts {
		if artifact.Module == "user" || artifact.Module == "project" {
			continue
		}
		switch artifact.Kind {
		case "skill":
			plan.Skills = append(plan.Skills, artifact.Name)
			// Where an agent has no commands or subagents, they are rendered as skills; keep their kind.
			switch artifact.Properties["source_kind"] {
			case "command":
				plan.Commands = append(plan.Commands, artifact.Name)
			case "agent":
				plan.Agents = append(plan.Agents, artifact.Name)
			}
		case "command":
			plan.Commands = append(plan.Commands, artifact.Name)
		case "agent":
			plan.Agents = append(plan.Agents, artifact.Name)
		}
	}
	receipt, err := entry.Probe(ctx, base.ProbeRequest{DefinitionRoot: definitionRoot, WorkspaceRoot: workspaceRoot, Mode: mode, SourceIdentity: report.SourceIdentity, LockDigest: report.LockDigest, Packaged: plan})
	if err != nil {
		return 5, err
	}

	if receipt.Coverage == nil {
		receipt.Coverage = []environment.CoverageNote{}
	}
	if receipt.Properties == nil {
		receipt.Properties = []environment.ObservedProperty{}
	}
	if receipt.Findings == nil {
		receipt.Findings = []environment.Finding{}
	}
	for i := range receipt.Properties {
		property := &receipt.Properties[i]
		if property.Evidence == environment.EvidenceObserved {
			property.EvidenceDigest, err = environment.ExecutableIdentity(property.EvidenceRef)
			if err != nil {
				return 5, fmt.Errorf("retain native observation evidence: %w", err)
			}
		}
	}
	after, err := runner.Service.Preflight(definitionRoot, packSync.PreflightOptions{Mode: mode, Target: &target, Policy: policy, WorkspaceRoot: workspaceRoot, StrictExternal: strict})
	if err != nil {
		return 5, err
	}
	if after.SourceIdentity != report.SourceIdentity || after.LockDigest != report.LockDigest || after.WorkspaceDigest != report.WorkspaceDigest || after.GenerationID != report.GenerationID || after.PolicyDigest != report.PolicyDigest {
		receipt.OverallStatus = "error"
		receipt.Termination.Status = "failed"
		receipt.Findings = append(receipt.Findings, environment.Finding{Code: "INPUTS_CHANGED", Severity: environment.SeverityError, Evidence: environment.EvidenceUnknown, Message: "environment inputs changed during native observation", Remedy: "restore and probe again"})
	}
	receipt.WorkspaceDigest = report.WorkspaceDigest
	receipt.GenerationID = report.GenerationID
	receipt.PolicyDigest = report.PolicyDigest
	receipt = environment.NormalizeReceipt(receipt)
	path, err := environment.SaveReceipt(workspaceRoot, receipt)
	if err != nil {
		return 1, err
	}
	if jsonOut {
		if err := writeJSON(runner.Stdout, receipt); err != nil {
			return 1, err
		}
	} else if !quiet {
		fmt.Fprintf(runner.Stdout, "probe status=%s receipt=%s\n", receipt.OverallStatus, path)
		fmt.Fprintf(runner.Stdout, "target\t%s\tversion=%s\n", receipt.Target.Adapter, receipt.Target.NativeVersion)
		for _, note := range receipt.Coverage {
			fmt.Fprintf(runner.Stdout, "coverage\t%s\t%s\t%s\n", note.Category, note.Completeness, note.Reason)
		}
		for _, finding := range receipt.Findings {
			fmt.Fprintf(runner.Stdout, "%s\t%s\t%s\n", finding.Severity, finding.Code, finding.Message)
		}
	}
	switch receipt.OverallStatus {
	case "ready", "ready_with_warnings":
		return 0, nil
	case "violated":
		return 3, fmt.Errorf("probe policy violated")
	case "unknown":
		return 4, fmt.Errorf("probe observation unknown")
	case "error":
		return 5, fmt.Errorf("probe operational failure")
	default:
		return 1, fmt.Errorf("probe failed")
	}
}
