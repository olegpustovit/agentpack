package claude

import "testing"

func TestParseUsageKeepsPrintedWindowsAndResetProse(t *testing.T) {
	output := `You are currently using your subscription to power your Claude Code usage

Current session: 5% used · resets Oct 30, 2:40pm (Europe/Zagreb)
Current week (all models): 34% used · resets Nov 4, 8am (Europe/Zagreb)
Current week (Fable): 0% used · resets Nov 4, 8am (Europe/Zagreb)

Last 24h · 429 requests · 4 sessions
`
	windows := parseUsage(output)
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(windows), windows)
	}
	if windows[0].Label != "Current session" || *windows[0].RemainingPercent != 95 {
		t.Fatalf("session = %+v", windows[0])
	}
	if windows[0].ResetText != "Oct 30, 2:40pm (Europe/Zagreb)" || windows[0].ResetAt != nil {
		t.Fatalf("reset must stay printed prose: %+v", windows[0])
	}
	if *windows[2].UsedPercent != 0 || windows[2].ID != "current-week-fable" {
		t.Fatalf("measured zero or scope lost: %+v", windows[2])
	}
}
