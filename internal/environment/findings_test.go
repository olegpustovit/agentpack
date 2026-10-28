package environment

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeReportFindingsAddsOnlyKnownStructureAndSafeArgv(t *testing.T) {
	report := NormalizeReport(Report{
		Target:          "claude",
		DefinitionRoot:  "/work/project",
		WorkspaceRoot:   "/work/project",
		Mode:            "review",
		Artifacts:       []ArtifactRecord{{ID: "package:helper", Name: "helper", Omitted: true}},
		ContractResults: []ContractResult{{RequirementID: "no-shadowing", FindingCode: "CONFIG_SHADOWED"}},
		Findings: []Finding{
			{Code: "CONFIG_SHADOWED", Severity: SeverityWarning, Source: "helper", Message: "shadowed", Remedy: "rename it; never run `sh -c anything`"},
			{Code: "CACHE_INTEGRITY", Severity: SeverityError, Message: "digest mismatch", Remedy: "run it", ArtifactID: "known"},
		},
	})
	shadowed, integrity := report.Findings[0], report.Findings[1]
	if shadowed.Consequence != "Agentpack omitted the packaged output because an ambient artifact wins for this target." || shadowed.Property != "winner" || shadowed.ArtifactID != "package:helper" || !reflect.DeepEqual(shadowed.RelatedRefs, []string{"contract:no-shadowing"}) {
		t.Fatalf("known shadowing metadata missing: %#v", shadowed)
	}
	if shadowed.Remediation == nil || shadowed.Remediation.Message != shadowed.Remedy || len(shadowed.Remediation.Argv) != 0 {
		t.Fatalf("legacy remedy was not preserved safely: %#v", shadowed.Remediation)
	}
	wantArgv := []string{"agentpack", "--definition-root", "/work/project", "--workspace", "/work/project", "--mode", "review", "sync", "--repair"}
	if !reflect.DeepEqual(integrity.Remediation.Argv, wantArgv) || integrity.ArtifactID != "known" {
		t.Fatalf("known safe repair command or explicit artifact reference changed: %#v", integrity)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Report
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.Findings[1].Remediation.Argv; !reflect.DeepEqual(got, integrity.Remediation.Argv) {
		t.Fatalf("JSON lost remediation argv: %v", got)
	}
}

func TestNormalizeReceiptPreservesLegacyRemedy(t *testing.T) {
	receipt := NormalizeReceipt(Receipt{Target: ReceiptTarget{Adapter: "codex"}, Findings: []Finding{{Code: "RECEIPT_STALE", Remedy: "probe", Message: "stale"}}})
	finding := receipt.Findings[0]
	if finding.Remedy != "probe" || finding.Remediation == nil || finding.Remediation.Message != "probe" || len(finding.Remediation.Argv) != 0 {
		t.Fatalf("receipt finding normalization: %#v", finding)
	}
}

func TestNormalizeFindingFillsEmptyStructuredMessageFromLegacyRemedy(t *testing.T) {
	report := NormalizeReport(Report{Findings: []Finding{{Code: "OTHER", Remedy: "legacy text", Remediation: &Remediation{}}}})
	finding := report.Findings[0]
	if finding.Remedy != "legacy text" || finding.Remediation.Message != "legacy text" {
		t.Fatalf("legacy remedy was lost: %#v", finding)
	}
}
