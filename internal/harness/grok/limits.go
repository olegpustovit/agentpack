package grok

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const limitsSource = "grok agent stdio _x.ai/billing"

type billingEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result *struct {
		Config *struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				End string `json:"end"`
			} `json:"currentPeriod"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
		} `json:"config"`
		SubscriptionTier string `json:"subscription_tier"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ReadLimits asks the signed-in Grok CLI for included-credit usage over ACP. It makes no REST or
// browser-auth calls. A wallet without a utilization value is not reported as 0% usage.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "grok", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	exe, err := base.ResolveBinary("GROK_PATH", "grok")
	if err != nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "grok CLI not found"
		return snapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "agent", "stdio")
	cmd.Env = os.Environ()
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if err := cmd.Start(); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1","clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"_x.ai/billing","params":{}}`,
	}
	for _, request := range requests {
		if _, err := io.WriteString(stdin, request+"\n"); err != nil {
			snapshot.Error = err.Error()
			return snapshot
		}
	}
	result, err := awaitBilling(ctx, stdout, "2")
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	windows, tier, err := parseBilling(result)
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Windows = windows
	snapshot.Plan = tier
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func awaitBilling(ctx context.Context, stdout io.Reader, id string) (json.RawMessage, error) {
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for lines.Scan() {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("grok agent timed out")
		}
		var message billingEnvelope
		if err := json.Unmarshal(bytes.TrimSpace(lines.Bytes()), &message); err != nil || len(message.ID) == 0 {
			continue
		}
		if strings.Trim(string(message.ID), `"`) != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("grok billing: %s", message.Error.Message)
		}
		raw, err := json.Marshal(message.Result)
		return raw, err
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("grok agent closed before responding")
}

func parseBilling(raw json.RawMessage) ([]base.LimitWindow, string, error) {
	var envelope billingEnvelope
	if err := json.Unmarshal(raw, &envelope.Result); err != nil || envelope.Result == nil {
		return nil, "", fmt.Errorf("unexpected billing response")
	}
	result := envelope.Result
	if result.Config == nil || result.Config.CreditUsagePercent == nil {
		return nil, result.SubscriptionTier, fmt.Errorf("grok billing reported no credit utilization")
	}
	used := min(max(*result.Config.CreditUsagePercent, 0), 100)
	remaining := 100 - used
	window := base.LimitWindow{
		ID:               "included-credits",
		Label:            "Included credits",
		UsedPercent:      &used,
		RemainingPercent: &remaining,
	}
	end := result.Config.BillingPeriodEnd
	if result.Config.CurrentPeriod != nil && result.Config.CurrentPeriod.End != "" {
		end = result.Config.CurrentPeriod.End
	}
	if resetAt, err := time.Parse(time.RFC3339, end); err == nil {
		window.ResetAt = &resetAt
	}
	return []base.LimitWindow{window}, result.SubscriptionTier, nil
}
