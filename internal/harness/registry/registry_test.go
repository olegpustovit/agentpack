package registry

import (
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func TestRegistryContainsEveryHarnessOnce(t *testing.T) {
	entries := All()
	if len(entries) != len(base.AllTargets()) {
		t.Fatalf("%d entries for %d targets", len(entries), len(base.AllTargets()))
	}
	for _, target := range base.AllTargets() {
		count := 0
		for _, entry := range entries {
			if entry.Target != target {
				continue
			}
			count++
			if entry.Hooks != nil && entry.Hooks.Target() != target {
				t.Fatalf("%s hook renderer names %s", target, entry.Hooks.Target())
			}
			if entry.Limits == nil || entry.Login == nil || entry.Launch == nil || entry.LaunchACP == nil {
				t.Fatalf("%s entry is incomplete", target)
			}
		}
		if count != 1 {
			t.Fatalf("%s has %d entries", target, count)
		}
	}
}
