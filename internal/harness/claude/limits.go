package claude

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const limitsSource = "claude -p /usage"

var usageLine = regexp.MustCompile(`^(Current [^:]+):\s*([0-9]+(?:\.[0-9]+)?)%\s*used\s*·\s*resets\s+(.+?)\s*$`)

// ReadLimits runs the signed-in Claude CLI's /usage report non-interactively and parses the
// windows it prints. Reset times are kept as the CLI's own prose, never converted to clock values.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "claude", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	exe, err := base.LookPathEnv("CLAUDE_CODE_PATH", "claude")
	if err != nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "claude CLI not found"
		return snapshot
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{
		Executable: exe,
		Argv:       []string{"-p", "/usage"},
		Timeout:    20 * time.Second,
		Categories: []string{"usage"},
		Network:    "claude_service",
		WriteScope: "none",
	})
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if result.ExitCode != 0 {
		snapshot.Error = fmt.Sprintf("claude exited with code %d", result.ExitCode)
		return snapshot
	}
	snapshot.Windows = parseUsage(result.Stdout)
	if len(snapshot.Windows) == 0 {
		snapshot.Error = "claude /usage reported no windows"
		return snapshot
	}
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func parseUsage(output string) []base.LimitWindow {
	var windows []base.LimitWindow
	for _, line := range strings.Split(output, "\n") {
		match := usageLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		used, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			continue
		}
		used = min(max(used, 0), 100)
		remaining := 100 - used
		label := match[1]
		windows = append(windows, base.LimitWindow{
			ID:               strings.ToLower(strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(label)),
			Label:            label,
			UsedPercent:      &used,
			RemainingPercent: &remaining,
			ResetText:        match[3],
		})
	}
	return windows
}
