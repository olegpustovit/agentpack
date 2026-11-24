package grok

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/obs"
)

const limitsSource = "grok agent stdio _x.ai/billing"

// ReadLimits asks the signed-in Grok CLI for included-credit usage over ACP. It makes no REST or
// browser-auth calls. A wallet without a utilization value is not reported as 0% usage.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot, exe := base.ProbeLimits(base.Grok, limitsSource)
	if exe == "" {
		return snapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "agent", "stdio")
	cmd.Env = os.Environ()
	if os.Getenv("GROK_HOME") == "" {
		home, err := os.MkdirTemp("", "agentpack-grok-limits-")
		if err != nil {
			snapshot.Error = "create limits home: " + err.Error()
			return snapshot
		}
		defer os.RemoveAll(home)
		shared, lock, err := lockSharedCredentials()
		if err != nil {
			snapshot.Error = "lock limits credentials: " + err.Error()
			return snapshot
		}
		defer lock.Unlock()
		if err := loadCredentials(home, shared); err != nil {
			snapshot.Error = "prepare limits credentials: " + err.Error()
			return snapshot
		}
		// Runs after the process below has stopped, and before the home is removed.
		loaded := make(map[string][32]byte, len(credentialFiles))
		for _, name := range credentialFiles {
			data, err := os.ReadFile(filepath.Join(home, name))
			if err != nil && !os.IsNotExist(err) {
				snapshot.Error = "read limits credentials: " + err.Error()
				return snapshot
			}
			loaded[name] = sha256.Sum256(data)
		}
		defer func() {
			if err := keepRefreshedCredentials(home, shared, loaded); err != nil {
				_, event := obs.Start(obs.ProcessTrace(ctx), "harness", "persist Grok limits credentials")
				event.Fail(err)
				event.End()
			}
		}()
		cmd.Env = append(cmd.Env, "GROK_HOME="+home)
	}
	rpc, err := base.StartStdioRPC(ctx, cmd, "grok agent", "grok billing")
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	defer rpc.Close()

	// Grok loads its login while answering initialize, so billing asked any sooner fails with "Internal error".
	if err := rpc.Send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1","clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}`); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if _, err := rpc.Await("1"); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if err := rpc.Send(`{"jsonrpc":"2.0","id":2,"method":"_x.ai/billing","params":{}}`); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	result, err := rpc.Await("2")
	if err != nil {
		snapshot.Error = err.Error()
		if strings.Contains(err.Error(), "Authentication required") {
			snapshot.Error = "Grok login expired. Sign in with agentpack grok login."
		}
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

func parseBilling(raw json.RawMessage) ([]base.LimitWindow, string, error) {
	var result *struct {
		Config *struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				End string `json:"end"`
			} `json:"currentPeriod"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
		} `json:"config"`
		SubscriptionTier string `json:"subscription_tier"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, "", fmt.Errorf("unexpected billing response")
	}
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
