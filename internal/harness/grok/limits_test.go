package grok

import (
	"encoding/json"
	"testing"
)

func TestParseBillingMapsIncludedCredits(t *testing.T) {
	raw := json.RawMessage(`{"subscription_tier":"SuperGrok","config":{
		"creditUsagePercent":68,
		"currentPeriod":{"start":"2026-10-04T14:00:00Z","end":"2026-11-04T14:00:00Z"},
		"billingPeriodEnd":"2026-11-30T00:00:00Z",
		"prepaidBalance":{"val":1446}}}`)
	windows, tier, err := parseBilling(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tier != "SuperGrok" || len(windows) != 1 {
		t.Fatalf("tier=%q windows=%+v", tier, windows)
	}
	if *windows[0].UsedPercent != 68 || *windows[0].RemainingPercent != 32 {
		t.Fatalf("window = %+v", windows[0])
	}
	if windows[0].ResetAt == nil || windows[0].ResetAt.Format("2006-01-02") != "2026-11-04" {
		t.Fatalf("reset must use the current period end: %+v", windows[0].ResetAt)
	}
}

func TestParseBillingWalletWithoutUtilizationIsNotZeroUsage(t *testing.T) {
	raw := json.RawMessage(`{"config":{"prepaidBalance":{"val":1446}}}`)
	windows, _, err := parseBilling(raw)
	if err == nil || len(windows) != 0 {
		t.Fatalf("balance-only must not become a window: %+v err=%v", windows, err)
	}
}

func TestParseBillingLiveShape(t *testing.T) {
	raw := json.RawMessage(`{"config":{"creditUsagePercent":86.0,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-26T18:01:54.666+00:00","end":"2026-11-02T18:01:54.666+00:00"},"prepaidBalance":{"val":0},"billingPeriodEnd":"2026-11-02T18:01:54.666+00:00"},"subscription_tier":"SuperGrok"}`)
	windows, tier, err := parseBilling(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tier != "SuperGrok" || *windows[0].UsedPercent != 86 || *windows[0].RemainingPercent != 14 || windows[0].ResetAt == nil {
		t.Fatalf("tier=%q window=%+v", tier, windows[0])
	}
}
