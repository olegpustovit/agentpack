package omp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "omp", State: base.LimitStateError, Source: "omp usage --json --no-extensions", ObservedAt: time.Now().UTC()}
	binary, err := base.LookPathEnv("OMP_PATH", "omp")
	if err != nil {
		snapshot.State, snapshot.Error = base.LimitStateUnsupported, "omp CLI not found"
		return snapshot
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"usage", "--json", "--no-extensions"}, Timeout: 20 * time.Second, Categories: []string{"usage"}, Network: "provider_usage", WriteScope: "native_usage_cache"})
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	windows, err := parseLimits([]byte(result.Stdout))
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Windows = windows
	if len(windows) == 0 {
		snapshot.State, snapshot.Error = base.LimitStateUnsupported, "OMP has no provider usage windows for this profile"
		return snapshot
	}
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func parseLimits(data []byte) ([]base.LimitWindow, error) {
	var response struct {
		Reports []struct {
			Provider string `json:"provider"`
			Limits   []struct {
				ID     string `json:"id"`
				Label  string `json:"label"`
				Amount struct {
					UsedFraction      *float64 `json:"usedFraction"`
					RemainingFraction *float64 `json:"remainingFraction"`
					Used              *float64 `json:"used"`
					Remaining         *float64 `json:"remaining"`
					Limit             *float64 `json:"limit"`
					Unit              string   `json:"unit"`
				} `json:"amount"`
				Window *struct {
					DurationMs *int64 `json:"durationMs"`
					ResetsAt   *int64 `json:"resetsAt"`
				} `json:"window"`
			} `json:"limits"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("parse OMP usage: %w", err)
	}
	var windows []base.LimitWindow
	for index, report := range response.Reports {
		for _, limit := range report.Limits {
			window := base.LimitWindow{ID: fmt.Sprintf("%s:%d:%s", report.Provider, index+1, limit.ID), Label: fmt.Sprintf("%s account %d: %s", report.Provider, index+1, limit.Label)}
			amount := limit.Amount
			var used *float64
			switch {
			case amount.UsedFraction != nil:
				value := *amount.UsedFraction * 100
				used = &value
			case amount.RemainingFraction != nil:
				value := (1 - *amount.RemainingFraction) * 100
				used = &value
			case amount.Limit != nil && *amount.Limit > 0 && amount.Used != nil:
				value := *amount.Used / *amount.Limit * 100
				used = &value
			case amount.Limit != nil && *amount.Limit > 0 && amount.Remaining != nil:
				value := (1 - *amount.Remaining / *amount.Limit) * 100
				used = &value
			case amount.Unit == "percent" && amount.Used != nil:
				value := *amount.Used
				used = &value
			case amount.Unit == "percent" && amount.Remaining != nil:
				value := 100 - *amount.Remaining
				used = &value
			}
			if used != nil {
				value := min(max(*used, 0), 100)
				remaining := 100 - value
				window.UsedPercent, window.RemainingPercent = &value, &remaining
			}
			if limit.Window != nil {
				if limit.Window.ResetsAt != nil {
					reset := time.UnixMilli(*limit.Window.ResetsAt).UTC()
					window.ResetAt = &reset
				}
				if limit.Window.DurationMs != nil {
					seconds := *limit.Window.DurationMs / 1000
					window.DurationSeconds = &seconds
				}
			}
			windows = append(windows, window)
		}
	}
	return windows, nil
}
