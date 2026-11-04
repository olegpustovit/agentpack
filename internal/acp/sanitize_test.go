package acp

import "testing"

func TestStripControls(t *testing.T) {
	const osc52 = "\u001b]52;c;evil\u0007injected"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps_newline", "a\nb", "a\nb"},
		{"strips_cr_spoof", "safe\rFORGED", "safeFORGED"},
		{"strips_tab_spoof", "safe\tFORGED", "safeFORGED"},
		{"strips_c0", "a\x00b", "ab"},
		{"strips_c1", "a\u0085b", "ab"},
		{"preserves_printable_unicode", "café 世界", "café 世界"},
		{"osc52_fixture", osc52, "injected"},
		{"csi_color", "\x1b[31mred\x1b[0m", "red"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripControls(tc.in); got != tc.want {
				t.Fatalf("StripControls(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
