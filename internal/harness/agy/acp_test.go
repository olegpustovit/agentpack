package agy

import (
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func TestSupportsPromptSteeringVersionAndIdentityFence(t *testing.T) {
	antigravityMeta := map[string]any{"antigravity": map[string]any{"tier": true}}
	tests := []struct {
		name string
		info base.ACPInitializeInfo
		want bool
	}{
		{
			name: "antigravity 1.5.0 exact floor",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "1.5.0", CapabilitiesMeta: antigravityMeta},
			want: true,
		},
		{
			name: "antigravity 1.5.17 local release",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "1.5.17", CapabilitiesMeta: antigravityMeta},
			want: true,
		},
		{
			name: "antigravity v-prefixed version",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "v1.6.0"},
			want: true,
		},
		{
			name: "antigravity identified by capability meta",
			info: base.ACPInitializeInfo{AgentName: "custom-wrapper", AgentVersion: "1.5.17", CapabilitiesMeta: antigravityMeta},
			want: true,
		},
		{
			name: "antigravity 1.4.12 below floor",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "1.4.12", CapabilitiesMeta: antigravityMeta},
			want: false,
		},
		{
			name: "antigravity 1.5.0 prerelease below floor",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "1.5.0-rc.1", CapabilitiesMeta: antigravityMeta},
			want: false,
		},
		{
			name: "empty version rejected",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", CapabilitiesMeta: antigravityMeta},
			want: false,
		},
		{
			name: "unparseable version rejected",
			info: base.ACPInitializeInfo{AgentName: "Antigravity ACP Server", AgentVersion: "dev", CapabilitiesMeta: antigravityMeta},
			want: false,
		},
		{
			name: "unrelated server rejected even with high version",
			info: base.ACPInitializeInfo{AgentName: "Other ACP Server", AgentVersion: "2.0.0"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := supportsPromptSteering(tc.info); got != tc.want {
				t.Fatalf("supportsPromptSteering(%+v) = %t, want %t", tc.info, got, tc.want)
			}
		})
	}
}
