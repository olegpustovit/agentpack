package omp

import (
	"strings"
	"testing"
)

func TestLimitsKeepAccountsAndUnknownAmountsSeparate(t *testing.T) {
	data := `{"reports":[{"provider":"anthropic","metadata":{"email":"private@example.com"},"limits":[{"id":"five-hour","label":"5 hour","amount":{"usedFraction":0.25},"window":{"durationMs":18000000,"resetsAt":2000000000000}},{"id":"unknown","label":"Unknown","amount":{"unit":"tokens"}}]},{"provider":"anthropic","limits":[{"id":"five-hour","label":"5 hour","amount":{"used":50,"limit":100}}]}]}`
	windows, err := parseLimits([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 3 || windows[0].ID == windows[2].ID {
		t.Fatalf("windows = %#v", windows)
	}
	if *windows[0].UsedPercent != 25 || *windows[2].RemainingPercent != 50 || windows[1].UsedPercent != nil {
		t.Fatalf("amounts = %#v", windows)
	}
	if windows[0].ResetAt.UnixMilli() != 2000000000000 || *windows[0].DurationSeconds != 18000 {
		t.Fatal("window metadata missing")
	}
	for _, window := range windows {
		if strings.Contains(window.Label, "private") {
			t.Fatal("account identity exposed")
		}
	}
	if _, err := parseLimits([]byte("not json")); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
