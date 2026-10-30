package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const limitsSource = "opencode api experimental.usage.limits"

type usageMeter struct {
	Percent  float64 `json:"percent"`
	Limited  bool    `json:"limited"`
	ResetsAt float64 `json:"resetsAt"`
}

type usageLimitsResponse struct {
	Data *struct {
		Go *struct {
			Rolling usageMeter `json:"rolling"`
			Weekly  usageMeter `json:"weekly"`
			Monthly usageMeter `json:"monthly"`
		} `json:"go"`
	} `json:"data"`
}

// ReadLimits reads OpenCode's usage-limit operation from the signed-in local server.
// It only reads usage and sends no inference turns.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "opencode", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	exe, err := base.LookPathEnv("OPENCODE_PATH", "opencode")
	if err != nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "opencode CLI not found"
		return snapshot
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{
		Executable: exe,
		Argv:       []string{"api", "experimental.usage.limits"},
		Timeout:    15 * time.Second,
		Categories: []string{"usage"},
		Network:    "opencode_service",
		WriteScope: "none",
	})
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if result.ExitCode != 0 {
		snapshot.Error = fmt.Sprintf("opencode exited with code %d", result.ExitCode)
		return snapshot
	}
	var usage usageLimitsResponse
	if err := json.Unmarshal([]byte(result.Stdout), &usage); err != nil {
		snapshot.Error = "unexpected usage response: " + err.Error()
		return snapshot
	}
	if usage.Data == nil || usage.Data.Go == nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "no OpenCode Go usage limits for this account"
		return snapshot
	}
	goLimits := usage.Data.Go
	snapshot.Windows = []base.LimitWindow{
		meterWindow("go/rolling", "Go rolling", goLimits.Rolling),
		meterWindow("go/weekly", "Go weekly", goLimits.Weekly),
		meterWindow("go/monthly", "Go monthly", goLimits.Monthly),
	}
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func meterWindow(id, label string, meter usageMeter) base.LimitWindow {
	used := min(max(meter.Percent, 0), 100)
	remaining := 100 - used
	resetAt := time.UnixMilli(int64(meter.ResetsAt)).UTC()
	return base.LimitWindow{
		ID:               id,
		Label:            label,
		UsedPercent:      &used,
		RemainingPercent: &remaining,
		ResetAt:          &resetAt,
	}
}
