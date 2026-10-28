package environment

import (
	"fmt"
	"slices"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// Severity ranks a preflight finding for CI policy.
type Severity string

const (
	SeverityInfo      Severity = "info"
	SeverityWarning   Severity = "warning"
	SeverityError     Severity = "error"
	SeverityViolation Severity = "violation"
)

// Policy selects how inheritance and conversion findings are treated.
type Policy string

const (
	// PolicyLocal allows inherited user config (developer machines).
	PolicyLocal Policy = "local"
	// PolicyCI treats undeclared inheritance and conversion loss as violations.
	PolicyCI Policy = "ci"
)

func ParsePolicy(value string) (Policy, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "local", "inherit":
		return PolicyLocal, nil
	case "ci", "controlled":
		return PolicyCI, nil
	default:
		return "", fmt.Errorf("unknown preflight policy %q (want local or ci)", value)
	}
}

// EvidenceLevel grades how a property was established.
type EvidenceLevel string

const (
	EvidenceDeclared  EvidenceLevel = "declared"
	EvidenceGenerated EvidenceLevel = "generated"
	EvidenceObserved  EvidenceLevel = "observed"
	EvidenceUnknown   EvidenceLevel = "unknown"
)

// Finding is a stable, actionable preflight record.
type Finding struct {
	Code        string        `json:"code"`
	Severity    Severity      `json:"severity"`
	Source      string        `json:"source,omitempty"`
	Target      string        `json:"target,omitempty"`
	Winner      string        `json:"winner,omitempty"`
	Message     string        `json:"message"`
	Remedy      string        `json:"remedy,omitempty"`
	Consequence string        `json:"consequence,omitempty"`
	Remediation *Remediation  `json:"remediation,omitempty"`
	ArtifactID  string        `json:"artifact_id,omitempty"`
	Property    string        `json:"property,omitempty"`
	RelatedRefs []string      `json:"related_refs,omitempty"`
	Evidence    EvidenceLevel `json:"evidence,omitempty"`
	Accepted    bool          `json:"accepted,omitempty"`
}

// Remediation is a human instruction and, when a concrete safe command is
// known, its argument vector. Argv is never parsed from Remedy text.
type Remediation struct {
	Message string   `json:"message"`
	Argv    []string `json:"argv,omitempty"`
}

// ArtifactRecord is a planned package artifact identity for effective-plan reports.
type ArtifactRecord struct {
	Properties map[string]any `json:"properties,omitempty"`
	OutputPath string         `json:"output_path,omitempty"`
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Module     string         `json:"module,omitempty"`
	Winner     string         `json:"winner,omitempty"` // package | user | project | plugin
	Omitted    bool           `json:"omitted,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Evidence   EvidenceLevel  `json:"evidence,omitempty"`
}

// CoverageNote records observation completeness for a category.
type CoverageNote struct {
	Category     string `json:"category"`
	Scope        string `json:"scope"`
	Completeness string `json:"completeness"` // complete | partial | none
	Reason       string `json:"reason,omitempty"`
}

// Report is the shared human/JSON preflight payload.
type Report struct {
	SchemaVersion   int                `json:"schema_version"`
	Phase           string             `json:"phase"`
	Environment     string             `json:"environment,omitempty"`
	DefinitionRoot  string             `json:"definition_root"`
	WorkspaceRoot   string             `json:"workspace_root"`
	Mode            string             `json:"mode"`
	Target          string             `json:"target,omitempty"`
	Policy          Policy             `json:"policy"`
	StrictExternal  bool               `json:"strict_external"`
	WorkspaceDigest string             `json:"workspace_digest,omitempty"`
	GenerationID    string             `json:"generation_id,omitempty"`
	PolicyDigest    string             `json:"policy_digest,omitempty"`
	ExpectedTarget  ReceiptTarget      `json:"expected_target"`
	Properties      []ObservedProperty `json:"properties,omitempty"`
	SourceIdentity  string             `json:"source_identity,omitempty"`
	LockDigest      string             `json:"lock_digest,omitempty"`
	Skills          int                `json:"skills"`
	Plugins         int                `json:"plugins"`
	Shadowed        int                `json:"shadowed"`
	Inheritance     []string           `json:"inheritance,omitempty"`
	Capabilities    map[string]string  `json:"capabilities,omitempty"`
	PlannedWrites   []string           `json:"planned_writes,omitempty"`
	Artifacts       []ArtifactRecord   `json:"artifacts,omitempty"`
	Coverage        []CoverageNote     `json:"coverage,omitempty"`
	ContractResults []ContractResult   `json:"contract_results,omitempty"`
	Findings        []Finding          `json:"findings"`
	OverallStatus   string             `json:"overall_status"`
	OK              bool               `json:"ok"`
}

// NormalizeReport fills structured finding metadata that is known from the
// report, while retaining Remedy for existing consumers.
func NormalizeReport(report Report) Report {
	for i := range report.Findings {
		finding := &report.Findings[i]
		normalizeFinding(finding)
		for _, result := range report.ContractResults {
			if result.FindingCode == finding.Code && result.RequirementID != "" {
				ref := "contract:" + result.RequirementID
				if !slices.Contains(finding.RelatedRefs, ref) {
					finding.RelatedRefs = append(finding.RelatedRefs, ref)
				}
			}
		}
		if finding.ArtifactID == "" && finding.Source != "" {
			var match string
			ambiguous := false
			for _, artifact := range report.Artifacts {
				if artifact.ID != finding.Source && artifact.Name != finding.Source {
					continue
				}
				if match != "" {
					ambiguous = true
					break
				}
				match = artifact.ID
			}
			if !ambiguous {
				finding.ArtifactID = match
			}
		}
		if finding.Code == "CONFIG_SHADOWED" && finding.ArtifactID != "" {
			for _, artifact := range report.Artifacts {
				if artifact.ID == finding.ArtifactID && artifact.Omitted {
					finding.Consequence = "Agentpack omitted the packaged output because an ambient artifact wins for this target."
					break
				}
			}
		}
		target := report.Target
		if target == "" {
			target = report.ExpectedTarget.Adapter
		}
		setKnownRemediationArgv(finding, target, report.DefinitionRoot, report.WorkspaceRoot, report.Mode)
	}
	return report
}

// NormalizeReceipt applies the same compatibility mapping to persisted findings.
func NormalizeReceipt(receipt Receipt) Receipt {
	for i := range receipt.Findings {
		finding := &receipt.Findings[i]
		normalizeFinding(finding)
		for _, result := range receipt.ContractResults {
			if result.FindingCode == finding.Code && result.RequirementID != "" {
				ref := "contract:" + result.RequirementID
				if !slices.Contains(finding.RelatedRefs, ref) {
					finding.RelatedRefs = append(finding.RelatedRefs, ref)
				}
			}
		}
	}
	return receipt
}

func normalizeFinding(finding *Finding) {
	if finding.Remediation == nil && finding.Remedy != "" {
		finding.Remediation = &Remediation{Message: finding.Remedy}
	} else if finding.Remediation != nil {
		if finding.Remediation.Message == "" {
			finding.Remediation.Message = finding.Remedy
		} else {
			finding.Remedy = finding.Remediation.Message
		}
	}
	if finding.Consequence == "" {
		switch finding.Code {
		case "CONFIG_SHADOWED":
			finding.Consequence = "An ambient artifact shares this name; native precedence is not established."
			if finding.Property == "" {
				finding.Property = "winner"
			}
		case "CACHE_MISSING":
			finding.Consequence = "Preflight cannot verify the locked package inputs."
		case "CACHE_INTEGRITY":
			finding.Consequence = "The cached package does not match its recorded content digest."
		case "STAGING_DRIFT":
			finding.Consequence = "Staged files differ from the last recorded materialization."
		case "RECEIPT_STALE":
			finding.Consequence = "The saved observation does not establish the current environment state."
		case "OBSERVATION_UNAVAILABLE":
			finding.Consequence = "The required native evidence is unavailable or changed."
		case "WORKSPACE_WRITE_REQUIRED":
			finding.Consequence = "The selected target cannot run under strict external policy."
		}
	}
	if finding.Property == "" {
		switch finding.Code {
		case "CACHE_MISSING", "CACHE_INTEGRITY":
			finding.Property = "package_input"
		case "STAGING_DRIFT":
			finding.Property = "staged_content"
		case "RECEIPT_STALE":
			finding.Property = "freshness"
		case "OBSERVATION_UNAVAILABLE":
			finding.Property = "evidence"
		}
	}
}

func setKnownRemediationArgv(finding *Finding, target, definitionRoot, workspaceRoot, mode string) {
	if finding.Remediation == nil || finding.Remediation.Argv != nil || definitionRoot == "" || workspaceRoot == "" {
		return
	}
	selectors := []string{"--definition-root", definitionRoot, "--workspace", workspaceRoot}
	if mode != "" {
		selectors = append(selectors, "--mode", mode)
	}
	switch finding.Code {
	case "CACHE_MISSING":
		finding.Remediation.Argv = append([]string{"agentpack"}, append(selectors, "env", "restore")...)
	case "STAGING_DRIFT", "STAGING_INPUTS_STALE", "STAGING_INPUTS_UNKNOWN", "STAGING_NOT_RECORDED", "STAGING_UNREADABLE":
		command := []string{"sync"}
		if definitionRoot != workspaceRoot {
			command = []string{"env", "restore"}
		}
		finding.Remediation.Argv = append([]string{"agentpack"}, append(selectors, command...)...)
	case "CACHE_INTEGRITY":
		finding.Remediation.Argv = append([]string{"agentpack"}, append(selectors, "sync", "--repair")...)
	case "RECEIPT_STALE", "OBSERVATION_UNAVAILABLE", "RECEIPT_MISSING":
		if target == "claude" || target == "codex" || target == "opencode" {
			finding.Remediation.Argv = append([]string{"agentpack"}, append(selectors, "probe", "--agent", target)...)
		}
	}
}

// HasObservedCoverage reports whether a category has complete observed coverage.
func (report Report) HasObservedCoverage(category string) bool {
	return report.HasObservedCoverageScope(category, "native_catalog")
}

func (report Report) HasObservedCoverageScope(category, scope string) bool {
	for _, note := range report.Coverage {
		if strings.EqualFold(note.Category, category) && note.Completeness == "complete" && note.Scope == scope {
			for _, property := range report.Properties {
				if property.Category == category && (property.Scope == scope || (property.Scope == "" && scope == "native_catalog")) && property.Property == "catalog_complete" && valuesEqual(property.Value, true) && property.Evidence == EvidenceObserved && property.Method != "" {
					return true
				}
			}
		}
	}
	return false
}

// ExternalCapability describes whether an adapter can run without checkout writes.
func ExternalCapability(target base.Target) (status, note string) {
	switch target {
	case base.Claude:
		return "external", "uses --plugin-dir and --settings; no workspace overlay"
	case base.OpenCode:
		return "external", "uses OPENCODE_CONFIG_DIR; no workspace overlay"
	case base.Codex:
		return "external-home", "uses CODEX_HOME; native project instructions may still apply"
	case base.Grok:
		return "external-home", "uses GROK_HOME; also reads real ~/.claude and ~/.grok"
	case base.Cursor:
		return "workspace-write", "writes .cursor/agents when agent artifacts exist"
	case base.Agy:
		return "workspace-write", "writes .agents/plugins/agentpack-bundle"
	default:
		return "unknown", ""
	}
}
