package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olegpustovit/agentpack/internal/cache"
	"github.com/olegpustovit/agentpack/internal/environment"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/registry"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/staging"
)

func appendCacheFindings(report *environment.Report, lock lockfile.PackLock) bool {
	if err := cache.VerifyLockCacheIntegrity(lock); err != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "CACHE_INTEGRITY",
			Severity: environment.SeverityError,
			Message:  err.Error(),
			Remedy:   "run `agentpack sync --repair` (mutates cache) then re-run preflight",
		})
		return false
	} else if err := cache.VerifyLockCacheLayout(lock); err != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "CACHE_MISSING",
			Severity: environment.SeverityError,
			Message:  err.Error(),
			Remedy:   "run `agentpack sync` or `agentpack env restore` to fetch locked inputs, then re-run preflight offline",
		})
		return false
	}
	return true
}

func appendArtifactFindings(report *environment.Report) {
	for _, artifact := range report.Artifacts {
		if artifact.Omitted {
			continue
		}
		artifactTarget := report.Target
		if target, ok := artifact.Properties["target"].(string); ok {
			artifactTarget = target
		}
		severity := environment.SeverityWarning
		if report.Policy == environment.PolicyCI {
			severity = environment.SeverityViolation
		}
		if preserved, ok := artifact.Properties["scope_preserved"].(bool); ok && !preserved {
			report.Findings = append(report.Findings, environment.Finding{Code: "RULE_SCOPE_DEGRADED", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: "scoped rule rendered as an unscoped skill; native glob enforcement is not preserved", Evidence: environment.EvidenceGenerated, Remedy: "choose a native rule target, disable this rule in the selected mode, or explicitly accept conversion under policy local"})
		}
		if fields, ok := artifact.Properties["dropped_fields"].([]string); ok && len(fields) > 0 {
			report.Findings = append(report.Findings, environment.Finding{Code: "ARTIFACT_FIELDS_DROPPED", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: "native rendering drops fields: " + strings.Join(fields, ", "), Evidence: environment.EvidenceGenerated, Remedy: "remove unsupported fields or choose a target that preserves them"})
		}
		if warnings, ok := artifact.Properties["warnings"].([]string); ok {
			for _, warning := range warnings {
				report.Findings = append(report.Findings, environment.Finding{Code: "MCP_RENDER_WARNING", Severity: severity, Source: artifact.ID, Target: artifactTarget, Message: warning, Evidence: environment.EvidenceGenerated, Remedy: "remove unsupported MCP fields or choose a target that preserves them"})
			}
		}
	}
}

func appendInheritanceFindings(report *environment.Report, options PreflightOptions) {
	report.Inheritance = inheritanceNotes(options.Target, options.Policy)
	for _, note := range report.Inheritance {
		report.Findings = append(report.Findings, environment.Finding{Code: "INHERITANCE_BOUNDARY", Severity: environment.SeverityInfo, Message: note, Evidence: environment.EvidenceDeclared})
	}
	appendDetectedInheritance(report, options)
}

func appendOverlayFindings(report *environment.Report, options PreflightOptions) {
	if options.StrictExternal {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STRICT_EXTERNAL",
			Severity: environment.SeverityInfo,
			Message:  "strict external mode refuses workspace overlay writes",
			Remedy:   "launch claude or opencode, or omit --strict-external for compatibility mode",
		})
		if options.Target != nil && options.Target.UsesWorkspaceOverlay() {
			status, note := environment.ExternalCapability(*options.Target)
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "WORKSPACE_WRITE_REQUIRED",
				Severity: environment.SeverityViolation,
				Target:   string(*options.Target),
				Message:  fmt.Sprintf("%s requires a workspace write (%s)", *options.Target, note),
				Remedy:   "choose claude/opencode, or run without --strict-external (compatibility mode)",
				Winner:   status,
			})
			report.PlannedWrites = append(report.PlannedWrites, plannedOverlayWrite(*options.Target, report.WorkspaceRoot)...)
		}
	} else if options.Target != nil && options.Target.UsesWorkspaceOverlay() {
		writes := plannedOverlayWrite(*options.Target, report.WorkspaceRoot)
		report.PlannedWrites = append(report.PlannedWrites, writes...)
		for _, write := range writes {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "WORKSPACE_WRITE_PLANNED",
				Severity: environment.SeverityWarning,
				Target:   string(*options.Target),
				Source:   write,
				Message:  "compatibility mode will write a workspace overlay",
				Remedy:   "add the path to .gitignore, or use --strict-external with claude/opencode",
			})
		}
	}
}

func appendMCPFindings(report *environment.Report, lock lockfile.PackLock, project *manifest.Manifest) {
	for _, server := range lock.MCPServers {
		switch server.Status {
		case lockfile.MCPUnpinned, lockfile.MCPUnpinnable:
			severity := environment.SeverityWarning
			if report.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "MCP_UNPINNED",
				Severity: severity,
				Source:   "mcp:" + server.Name,
				Message:  fmt.Sprintf("MCP server %s is %s", server.Name, server.Status),
				Remedy:   "pin with `agentpack lock`, or accept explicitly under policy local",
			})
		}
	}
	if project != nil {
		for name, server := range project.MCP.Servers {
			for key := range server.Env {
				if !looksLikeSecret(key) {
					continue
				}
				if os.Getenv(key) == "" {
					report.Findings = append(report.Findings, environment.Finding{
						Code:     "SECRET_NAME_MISSING",
						Severity: environment.SeverityWarning,
						Source:   "mcp:" + name,
						Message:  fmt.Sprintf("required environment variable %s is unset (value not reported)", key),
						Remedy:   "export " + key + " in the process environment before launch",
					})
				}
			}
		}
	}
}

func appendTargetFindings(report *environment.Report, target *base.Target, hookPlans []hooks.TargetPlan) {
	if target != nil {
		if path, lookErr := base.ResolveBinary(target.Binary()); lookErr != nil {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "BINARY_MISSING",
				Severity: environment.SeverityError,
				Target:   string(*target),
				Message:  lookErr.Error(),
				Remedy:   "install the native CLI or set the harness path env var",
			})
		} else {
			report.Findings = append(report.Findings, environment.Finding{
				Code:     "BINARY_FOUND",
				Severity: environment.SeverityInfo,
				Target:   string(*target),
				Source:   path,
				Message:  "native CLI located",
			})
		}
		for _, plan := range hookPlans {
			report.Findings = append(report.Findings, hookFindings(plan)...)
		}
	}
}

func appendStagingFindings(report *environment.Report, pipeline staging.Pipeline, lockDigest string) error {
	definitionRoot, workspace, effective := pipeline.ProjectRoot, pipeline.WorkspaceRoot, pipeline.Mode
	materializeRemedy := "run `agentpack sync` to materialize current inputs before probing"
	if filepath.Clean(definitionRoot) != filepath.Clean(workspace) {
		materializeRemedy = "run `agentpack env restore` to publish the selected frozen environment before probing"
	}
	// A full sync materializes every target, so it also answers for one.
	recordedFor := pipeline.Target
	materialized, materializedFound, materializedErr := readMaterializationDigest(definitionRoot, effective.Name(), recordedFor)
	if materializedErr == nil && !materializedFound && recordedFor != nil {
		recordedFor = nil
		materialized, materializedFound, materializedErr = readMaterializationDigest(definitionRoot, effective.Name(), nil)
	}
	if materializedErr != nil {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_UNREADABLE", Severity: environment.SeverityError, Message: materializedErr.Error(), Evidence: environment.EvidenceUnknown, Remedy: materializeRemedy})
	} else if !materializedFound {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_INPUTS_UNKNOWN", Severity: environment.SeverityWarning, Message: "no materialization input identity is recorded", Evidence: environment.EvidenceUnknown, Remedy: materializeRemedy})
	} else if current, err := computeMaterializationDigest(definitionRoot, workspace, effective, recordedFor, lockDigest); err != nil {
		return err
	} else if materialized != current {
		report.Findings = append(report.Findings, environment.Finding{Code: "STAGING_INPUTS_STALE", Severity: environment.SeverityViolation, Message: "staged configuration was built from different definition, mode, workspace, or inherited inputs", Evidence: environment.EvidenceGenerated, Remedy: materializeRemedy})
	}
	drift, recorded, driftErr := pipeline.StagedDrift(true)
	if driftErr != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_UNREADABLE",
			Severity: environment.SeverityError,
			Message:  driftErr.Error(),
			Remedy:   materializeRemedy,
		})
	} else if !recorded {
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_NOT_RECORDED",
			Severity: environment.SeverityWarning,
			Message:  fmt.Sprintf("no staged record for mode %q", effective.Name()),
			Remedy:   materializeRemedy,
		})
	} else if len(drift) != 0 {
		limit := min(len(drift), 5)
		report.Findings = append(report.Findings, environment.Finding{
			Code:     "STAGING_DRIFT",
			Severity: environment.SeverityError,
			Message:  fmt.Sprintf("%d staged file(s) differ from the last recorded sync", len(drift)),
			Source:   strings.Join(drift[:limit], "; "),
			Remedy:   materializeRemedy,
		})
	}
	return nil
}

func markArtifactWinner(report *environment.Report, name, winner, reason string) {
	for index := range report.Artifacts {
		artifact := &report.Artifacts[index]
		if artifact.Kind != "skill" || artifact.Module == "user" || artifact.Module == "project" || !strings.EqualFold(artifact.Name, name) {
			continue
		}
		artifact.Winner = winner
		artifact.Omitted = true
		artifact.Reason = reason
		artifact.Evidence = environment.EvidenceGenerated
	}
}

func appendAmbientFindings(report *environment.Report, workspace string, options PreflightOptions) {
	packSkills := map[string]string{}
	for _, artifact := range report.Artifacts {
		if artifact.Kind == "skill" && !artifact.Omitted {
			packSkills[strings.ToLower(artifact.Name)] = artifact.Name
		}
	}
	home, _ := os.UserHomeDir()
	userSkills := make(map[string]struct{})
	userTargets := make(map[string][]string)
	candidates := registry.All()
	for _, candidate := range candidates {
		if options.Target != nil && candidate.Target != *options.Target {
			continue
		}
		names, err := staging.UserSkillNames(candidate.NativeUserRoots(home))
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityWarning,
				Target: string(candidate.Target), Message: err.Error(), Evidence: environment.EvidenceUnknown,
				Remedy: "fix permissions on the target's native user skill directories",
			})
			return
		}
		for name := range names {
			userSkills[name] = struct{}{}
			userTargets[name] = append(userTargets[name], string(candidate.Target))
		}
	}
	projectSkills := make(map[string]struct{})
	shadowingRoots := make(map[string]string)
	projectRoots := []string{".agents/skills"}
	for _, candidate := range candidates {
		if candidate.ProjectSkillRoot == "" || options.Target != nil && *options.Target != candidate.Target {
			continue
		}
		if !candidate.ProjectSkillsShadow {
			projectRoots = append(projectRoots, candidate.ProjectSkillRoot)
			continue
		}
		names, err := staging.ShadowingProjectSkillNames(workspace, candidate.ProjectSkillRoot)
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Message: err.Error(), Evidence: environment.EvidenceUnknown})
			return
		}
		for name := range names {
			projectSkills[name] = struct{}{}
			shadowingRoots[name] = candidate.ProjectSkillRoot
		}
	}
	for _, layer := range []struct {
		names  map[string]struct{}
		winner string
	}{{userSkills, "user"}, {projectSkills, "project"}} {
		keys := make([]string, 0, len(layer.names))
		for key := range layer.names {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, lower := range keys {
			display := lower
			report.Artifacts = append(report.Artifacts, environment.ArtifactRecord{ID: layer.winner + ":" + display, Kind: "skill", Name: display, Module: layer.winner, Winner: layer.winner, Evidence: environment.EvidenceGenerated})
			if _, packaged := packSkills[lower]; !packaged {
				severity := environment.SeverityInfo
				if options.Policy == environment.PolicyCI {
					severity = environment.SeverityViolation
				}
				report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: layer.winner, Message: "undeclared skill exists in a native discovery root", Evidence: environment.EvidenceGenerated, Remedy: "use a controlled home/workspace for CI or narrow the policy to local"})
			}
		}
	}

	// Skills that .agents itself contributes are project content, not packaged skills.
	lockedSkills := map[string]string{}
	for _, artifact := range report.Artifacts {
		if artifact.Kind != "skill" || artifact.Module == ".agents" {
			continue
		}
		// The plan already omits a locked skill that .agents overrides; it is still a locked name.
		if !artifact.Omitted || strings.HasPrefix(artifact.Reason, "overridden by .agents:") {
			lockedSkills[strings.ToLower(artifact.Name)] = artifact.Name
		}
	}
	for _, root := range projectRoots {
		names, rootErr := staging.ProjectSkillNames(workspace, root)
		if rootErr != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Message: rootErr.Error(), Evidence: environment.EvidenceUnknown})
			return
		}
		for lower, display := range lockedSkills {
			if _, hit := names[lower]; !hit {
				continue
			}
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			alreadyProject := false
			for _, artifact := range report.Artifacts {
				alreadyProject = alreadyProject || (artifact.Kind == "skill" && artifact.Module == "project" && strings.EqualFold(artifact.Name, lower))
			}
			markArtifactWinner(report, display, "project", "project "+root+" has a skill with the packaged skill's name")
			if !alreadyProject {
				report.Artifacts = append(report.Artifacts, environment.ArtifactRecord{ID: "project:" + lower, Kind: "skill", Name: lower, Module: "project", Winner: "project", Evidence: environment.EvidenceGenerated})
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: "project",
				Message:  fmt.Sprintf("project %s/%s has the packaged skill's name", root, display),
				Remedy:   "rename or remove the project skill, disable the package in a mode, or accept under --policy local",
				Evidence: environment.EvidenceGenerated,
			})
		}
	}
	for lower, display := range packSkills {
		if _, hit := userSkills[lower]; hit {
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI || options.StrictExternal {
				severity = environment.SeverityViolation
			}
			if options.Target != nil {
				markArtifactWinner(report, display, "user", "user skill wins over packaged skill")
			}
			for _, target := range userTargets[lower] {
				report.Findings = append(report.Findings, environment.Finding{
					Code: "CONFIG_SHADOWED", Severity: severity, Source: display, Winner: "user", Target: target,
					Message:  fmt.Sprintf("user skill %q overrides the packaged skill for %s", display, target),
					Remedy:   "rename/remove the user skill, or accept under --policy local without --strict-external",
					Evidence: environment.EvidenceGenerated,
				})
			}
		}
		if root, hit := shadowingRoots[lower]; hit {
			severity := environment.SeverityWarning
			if options.Policy == environment.PolicyCI {
				severity = environment.SeverityViolation
			}
			markArtifactWinner(report, display, "project", "project "+root+" wins over packaged skill")
			report.Findings = append(report.Findings, environment.Finding{
				Code: "AMBIENT_ARTIFACT", Severity: severity, Source: display, Winner: "project",
				Message:  fmt.Sprintf("project %s/%s shadows the packaged skill", root, display),
				Remedy:   "remove the project skill, disable the package in a mode, or accept under --policy local",
				Evidence: environment.EvidenceGenerated,
			})
		}
	}
}

func applyReceipt(report *environment.Report, workspace string, options PreflightOptions) {
	receipt, err := environment.LoadReceipt(workspace, options.ReceiptID)
	if errors.Is(err, environment.ErrNoReceipt) {
		// Nothing has been observed yet: required observations stay unknown, the evidence is not damaged.
		report.Findings = append(report.Findings, environment.Finding{
			Code: "RECEIPT_MISSING", Severity: environment.SeverityWarning,
			Message: "no probe receipt exists for this workspace", Evidence: environment.EvidenceUnknown,
			Remedy: "run `agentpack probe --agent <target>` to observe the native configuration",
		})
		return
	}
	if err != nil {
		report.Findings = append(report.Findings, environment.Finding{
			Code: "RECEIPT_STALE", Severity: environment.SeverityError,
			Message: err.Error(), Evidence: environment.EvidenceUnknown,
			Remedy: "run `agentpack probe --agent <target>` to create a fresh receipt",
		})
		return
	}
	stale := receipt.SourceIdentity != report.SourceIdentity || receipt.LockDigest != report.LockDigest || receipt.Mode != report.Mode || receipt.WorkspaceDigest != report.WorkspaceDigest || receipt.GenerationID != report.GenerationID || receipt.PolicyDigest != report.PolicyDigest
	expected := report.ExpectedTarget
	stale = stale || expected.Adapter == "" || receipt.Target.Adapter != expected.Adapter || receipt.Target.AdapterRevision != expected.AdapterRevision || receipt.Target.ExecutableIdentity != expected.ExecutableIdentity || receipt.Target.CapabilityRevision != expected.CapabilityRevision
	if stale {
		report.Findings = append(report.Findings, environment.Finding{Code: "RECEIPT_STALE", Severity: environment.SeverityViolation, Message: "receipt identities do not match current definition, workspace inputs, generation, policy, or native adapter", Evidence: environment.EvidenceUnknown, Remedy: "run `agentpack probe --agent <target>` again after sync/restore"})
		return
	}
	if receipt.Termination.Status != "completed" || receipt.Termination.NativeExitCode != 0 || (receipt.OverallStatus != "ready" && receipt.OverallStatus != "ready_with_warnings") {
		report.Findings = append(report.Findings, environment.Finding{Code: "PROBE_FAILED", Severity: environment.SeverityError, Message: "receipt does not describe a successful completed probe", Evidence: environment.EvidenceUnknown, Remedy: "resolve probe failures and run probe again"})
		return
	}
	for _, prop := range receipt.Properties {
		if prop.Evidence != environment.EvidenceObserved {
			continue
		}
		info, statErr := os.Stat(prop.EvidenceRef)
		digest := ""
		var err error
		if statErr == nil && info.Mode().IsRegular() && info.Size() <= 2<<20 {
			digest, err = environment.ExecutableIdentity(prop.EvidenceRef)
		} else {
			err = fmt.Errorf("native evidence must be a bounded regular file")
		}
		if prop.EvidenceRef == "" || prop.EvidenceDigest == "" || err != nil || digest != prop.EvidenceDigest {
			report.Findings = append(report.Findings, environment.Finding{Code: "OBSERVATION_UNAVAILABLE", Severity: environment.SeverityError, Message: "native evidence is unavailable or differs from its recorded digest", Evidence: environment.EvidenceUnknown, Remedy: "run probe again to retain current diagnostic evidence"})
			return
		}
	}
	// Complete coverage alone never manufactures positive observations.
	report.Coverage = receipt.Coverage
	report.Properties = receipt.Properties

	for _, prop := range receipt.Properties {
		if prop.Evidence != environment.EvidenceObserved {
			continue
		}
		report.Findings = append(report.Findings, environment.Finding{
			Code: "NATIVE_OBSERVED", Severity: environment.SeverityInfo,
			Source: prop.ArtifactID, Message: fmt.Sprintf("observed %s=%v via %s", prop.Property, prop.Value, prop.Method),
			Evidence: environment.EvidenceObserved,
		})
	}
}

func applyContract(report *environment.Report, contract environment.Contract) {
	results := environment.EvaluateContract(contract, *report)
	report.ContractResults = results
	unknownMode := strings.ToLower(strings.TrimSpace(contract.UnknownRequired))
	if unknownMode == "" {
		unknownMode = "fail"
	}
	requirements := map[string]environment.Requirement{}
	for _, req := range contract.Requirements {
		requirements[req.ID] = req
	}
	for _, result := range results {
		reqSeverity := requirements[result.RequirementID].Severity
		violationSeverity := environment.SeverityViolation
		if reqSeverity == environment.SeverityInfo || reqSeverity == environment.SeverityWarning {
			violationSeverity = reqSeverity
		}
		switch result.Result {
		case "violated":
			report.Findings = append(report.Findings, environment.Finding{
				Code: result.FindingCode, Severity: violationSeverity,
				Message: result.Message, Remedy: "adjust the environment or add a scoped allowance with rationale",
				Evidence: environment.EvidenceGenerated,
			})
		case "unknown":
			severity := environment.SeverityWarning
			if unknownMode == "fail" {
				severity = violationSeverity
			} else if unknownMode == "allow" {
				severity = environment.SeverityInfo
			}
			code := result.FindingCode
			if code == "" {
				code = "OBSERVATION_UNAVAILABLE"
			}
			report.Findings = append(report.Findings, environment.Finding{
				Code: code, Severity: severity, Message: result.Message,
				Remedy:   "run an explicit native probe, or narrow the contract to generated evidence",
				Evidence: environment.EvidenceUnknown,
			})
		}
	}
}

func overallStatus(report environment.Report) string {
	hasViolation, hasError, hasBlockingUnknown, hasWarning := false, false, false, false
	for _, finding := range report.Findings {
		switch finding.Severity {
		case environment.SeverityViolation:
			hasViolation = true
		case environment.SeverityError:
			hasError = true
		case environment.SeverityWarning:
			hasWarning = true
		}
		if finding.Evidence == environment.EvidenceUnknown &&
			(finding.Severity == environment.SeverityViolation || finding.Severity == environment.SeverityError) {
			hasBlockingUnknown = true
		}
	}
	switch {
	case hasError:
		return "error"
	case hasBlockingUnknown:
		return "unknown"
	case hasViolation:
		return "violated"
	case hasWarning:
		return "ready_with_warnings"
	default:
		return "ready"
	}
}

func hookFindings(plan hooks.TargetPlan) []environment.Finding {
	target := string(plan.Target)
	var findings []environment.Finding
	for _, diagnostic := range plan.Skipped {
		findings = append(findings, environment.Finding{Code: "HOOK_SKIPPED", Severity: environment.SeverityWarning, Source: diagnostic.Source, Target: target, Message: diagnostic.Message, Remedy: "correct the hook definition or remove the unsupported event/handler"})
	}
	for _, diagnostic := range plan.Diagnostics {
		severity := environment.SeverityWarning
		code := "HOOK_DIAGNOSTIC"
		switch strings.ToLower(diagnostic.Level) {
		case "error":
			severity = environment.SeverityError
			code = "HOOK_ERROR"
		case "omitted", "unsupported":
			code = "HOOK_UNSUPPORTED"
		case "degraded":
			code = "HOOK_DEGRADED"
		}
		findings = append(findings, environment.Finding{
			Code:     code,
			Severity: severity,
			Source:   diagnostic.Source,
			Target:   target,
			Message:  diagnostic.Message,
			Remedy:   "disable the hook, choose a target with native support, or accept the finding under policy local",
		})
	}
	if plan.Summary.Omitted != 0 || plan.Summary.Degraded != 0 || plan.Summary.Emulated != 0 {
		findings = append(findings, environment.Finding{
			Code:     "HOOK_SUMMARY",
			Severity: environment.SeverityInfo,
			Target:   target,
			Message:  fmt.Sprintf("hooks: native=%d emulated=%d degraded=%d omitted=%d", plan.Summary.Native, plan.Summary.Emulated, plan.Summary.Degraded, plan.Summary.Omitted),
		})
	}
	return findings
}

func looksLikeSecret(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD")
}

func inheritanceNotes(target *base.Target, policy environment.Policy) []string {
	notes := []string{
		"credentials, remote MCP services, npm/npx installs, and model answers stay outside the portable closure",
	}
	if note := selectedHarness(target).Inheritance.Note; note != "" {
		notes = append(notes, note)
	}
	if policy == environment.PolicyCI {
		notes = append(notes, "CI policy treats undeclared inheritance as a violation")
	}
	return notes
}

func plannedOverlayWrite(target base.Target, workspace string) []string {
	var writes []string
	for _, name := range selectedHarness(&target).Inheritance.Overlay {
		writes = append(writes, filepath.Join(workspace, filepath.FromSlash(name)))
	}
	return writes
}

func appendDetectedInheritance(report *environment.Report, options PreflightOptions) {
	for _, path := range inheritedConfigPaths(options.Target, report.WorkspaceRoot) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			report.Findings = append(report.Findings, environment.Finding{Code: "AMBIENT_SCAN_FAILED", Severity: environment.SeverityError, Source: path, Message: "cannot inspect inherited configuration", Evidence: environment.EvidenceUnknown})
			continue
		}
		if !info.IsDir() && info.Size() == 0 {
			continue
		}
		severity := environment.SeverityWarning
		if options.Policy == environment.PolicyCI {
			severity = environment.SeverityViolation
		}
		report.Findings = append(report.Findings, environment.Finding{Code: "INHERITED_CONFIG", Severity: severity, Source: path, Message: "native launch may read existing undeclared configuration", Evidence: environment.EvidenceGenerated, Remedy: "use a controlled account/configuration home for CI, or use policy local"})
	}
}
