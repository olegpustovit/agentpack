package opencode

import (
	"encoding/json"
	"testing"
)

func TestMeterWindowKeepsSourceFactsOnly(t *testing.T) {
	var usage usageLimitsResponse
	raw := `{"location":{"directory":"/x"},"data":{"workspace":{"id":"w","usageURL":"u"},"budgets":[],"go":{
		"rolling":{"percent":120,"limited":true,"resetsAt":1793000000000},
		"weekly":{"percent":0,"limited":false,"resetsAt":1793500000000},
		"monthly":{"percent":10,"limited":false,"resetsAt":1795000000000}}}}`
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	rolling := meterWindow("go/rolling", "Go rolling", usage.Data.Go.Rolling)
	if *rolling.UsedPercent != 100 || *rolling.RemainingPercent != 0 {
		t.Fatalf("rolling = %+v", rolling)
	}
	if rolling.DurationSeconds != nil {
		t.Fatal("duration must not be invented")
	}
	weekly := meterWindow("go/weekly", "Go weekly", usage.Data.Go.Weekly)
	if *weekly.UsedPercent != 0 || weekly.ResetAt == nil {
		t.Fatalf("measured zero must survive: %+v", weekly)
	}
}
