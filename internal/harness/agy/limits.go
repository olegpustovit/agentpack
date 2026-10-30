package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const limitsSource = "agy -p /usage --output-format json"

type usageReport struct {
	Status  string `json:"status"`
	Command struct {
		Data struct {
			Groups []struct {
				Name    string `json:"name"`
				Buckets []struct {
					ID                string   `json:"id"`
					Name              string   `json:"name"`
					Window            string   `json:"window"`
					RemainingFraction *float64 `json:"remaining_fraction"`
					ResetTime         string   `json:"reset_time"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

// ReadLimits runs the signed-in agy CLI's structured /usage report. Each named group keeps its
// own buckets because model families do not share allowances.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "agy", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	exe, err := base.LookPathEnv("AGY_PATH", "agy")
	if err != nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "agy CLI not found"
		return snapshot
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{
		Executable: exe,
		Argv:       []string{"-p", "/usage", "--output-format", "json"},
		Timeout:    30 * time.Second,
		Categories: []string{"usage"},
		Network:    "agy_service",
		WriteScope: "none",
	})
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if result.ExitCode != 0 {
		snapshot.Error = fmt.Sprintf("agy exited with code %d", result.ExitCode)
		return snapshot
	}
	windows, err := parseReport(result.Stdout)
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Windows = windows
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func parseReport(stdout string) ([]base.LimitWindow, error) {
	var report usageReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, fmt.Errorf("unexpected usage response: %w", err)
	}
	if report.Status != "SUCCESS" {
		return nil, fmt.Errorf("agy usage report status %s", report.Status)
	}
	var windows []base.LimitWindow
	for _, group := range report.Command.Data.Groups {
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				continue
			}
			remaining := min(max(*bucket.RemainingFraction*100, 0), 100)
			used := 100 - remaining
			window := base.LimitWindow{
				ID:               group.Name + "/" + bucket.ID,
				Label:            group.Name + " · " + bucket.Name,
				UsedPercent:      &used,
				RemainingPercent: &remaining,
			}
			if resetAt, err := time.Parse(time.RFC3339, bucket.ResetTime); err == nil {
				window.ResetAt = &resetAt
			}
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("agy reported no usage buckets")
	}
	return windows, nil
}
