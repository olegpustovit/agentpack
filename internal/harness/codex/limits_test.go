package codex

import (
	"encoding/json"
	"testing"
)

func TestLimitWindowsMapsEveryBucket(t *testing.T) {
	raw := `{
		"rateLimits": null,
		"rateLimitsByLimitId": {
			"codex": {
				"limitId": "codex",
				"limitName": "Codex",
				"planType": "plus",
				"primary": {"usedPercent": 28, "windowDurationMins": 300, "resetsAt": 1793000000},
				"secondary": {"usedPercent": 62, "windowDurationMins": 10080, "resetsAt": 1793500000}
			},
			"codex_other": {
				"limitName": "Other",
				"primary": {"usedPercent": 0, "windowDurationMins": null, "resetsAt": null}
			}
		}
	}`
	var usage rateLimitsResponse
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	windows, plan := limitWindows(usage)
	if plan != "plus" {
		t.Fatalf("plan = %q", plan)
	}
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(windows))
	}
	if windows[0].Label != "Codex 5h" || *windows[0].RemainingPercent != 72 {
		t.Fatalf("first window = %+v", windows[0])
	}
	if *windows[1].DurationSeconds != 604800 || windows[1].Label != "Codex 7d" {
		t.Fatalf("second window = %+v", windows[1])
	}
	if windows[2].UsedPercent == nil || *windows[2].UsedPercent != 0 {
		t.Fatalf("measured zero must survive: %+v", windows[2])
	}
	if windows[2].DurationSeconds != nil || windows[2].ResetAt != nil {
		t.Fatalf("absent values must stay unknown: %+v", windows[2])
	}
}

func TestLimitWindowsFallsBackToSingleBucket(t *testing.T) {
	var usage rateLimitsResponse
	if err := json.Unmarshal([]byte(`{"rateLimits":{"primary":{"usedPercent":150}}}`), &usage); err != nil {
		t.Fatal(err)
	}
	windows, _ := limitWindows(usage)
	if len(windows) != 1 || *windows[0].UsedPercent != 100 || *windows[0].RemainingPercent != 0 {
		t.Fatalf("windows = %+v", windows)
	}
}
