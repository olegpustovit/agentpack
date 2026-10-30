package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	keyring "github.com/zalando/go-keyring"
)

// shortcut: Cursor’s bundled usage RPC is not public; update the fixtures if its schema changes.
const limitsSource = "Cursor DashboardService/GetCurrentPeriodUsage"

type currentPeriodUsage struct {
	BillingCycleEnd json.Number `json:"billingCycleEnd"`
	Enabled         bool        `json:"enabled"`
	PlanUsage       *struct {
		Limit            *float64 `json:"limit"`
		Remaining        *float64 `json:"remaining"`
		TotalPercentUsed *float64 `json:"totalPercentUsed"`
		AutoPercentUsed  *float64 `json:"autoPercentUsed"`
		APIPercentUsed   *float64 `json:"apiPercentUsed"`
	} `json:"planUsage"`
	SpendLimitUsage *struct {
		IndividualLimit     *float64 `json:"individualLimit"`
		IndividualRemaining *float64 `json:"individualRemaining"`
	} `json:"spendLimitUsage"`
}

// ReadLimits uses the same read-only usage RPC and credential store as Cursor CLI.
// It never starts an agent or sends an inference request.
func ReadLimits(ctx context.Context) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "cursor", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	token, err := usageToken()
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	if token == "" {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "No Cursor CLI login on this machine. Sign in with cursor-agent login."
		return snapshot
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage", strings.NewReader("{}"))
	if err != nil {
		snapshot.Error = "create Cursor usage request"
		return snapshot
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	response, err := client.Do(request)
	if err != nil {
		snapshot.Error = "Cursor usage service is unreachable"
		return snapshot
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			snapshot.Error = "Cursor login expired or has no usage access. Sign in with cursor-agent login."
		} else {
			snapshot.Error = fmt.Sprintf("Cursor usage service returned HTTP %d", response.StatusCode)
		}
		return snapshot
	}
	var usage currentPeriodUsage
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&usage); err != nil {
		snapshot.Error = "Unexpected Cursor usage response"
		return snapshot
	}
	snapshot.Windows = usageWindows(usage)
	if len(snapshot.Windows) == 0 {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "Cursor did not expose usage limits for this account"
		return snapshot
	}
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func usageToken() (string, error) {
	if token := os.Getenv("CURSOR_AUTH_TOKEN"); token != "" {
		return token, nil
	}
	if os.Getenv("AGENT_CLI_CREDENTIAL_STORE") == "memory" {
		return "", nil
	}
	if runtime.GOOS == "darwin" && os.Getenv("AGENT_CLI_CREDENTIAL_STORE") != "file" {
		token, err := keyring.Get("cursor-access-token", "cursor-user")
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return "", errors.New("Cursor login keychain is unavailable")
		}
		return token, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate Cursor login: %w", err)
	}
	var path string
	switch runtime.GOOS {
	case "darwin":
		path = filepath.Join(home, ".cursor", "auth.json")
	case "windows":
		root := os.Getenv("APPDATA")
		if root == "" {
			root = filepath.Join(home, "AppData", "Roaming")
		}
		path = filepath.Join(root, "Cursor", "auth.json")
	default:
		root := os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		path = filepath.Join(root, "cursor", "auth.json")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("could not read Cursor CLI credentials")
	}
	if err == nil {
		var auth struct {
			AccessToken string `json:"accessToken"`
		}
		if json.Unmarshal(data, &auth) != nil {
			return "", errors.New("invalid Cursor CLI credentials")
		}
		if auth.AccessToken != "" {
			return auth.AccessToken, nil
		}
	}

	return "", nil
}

func usageWindows(usage currentPeriodUsage) []base.LimitWindow {
	if !usage.Enabled {
		return nil
	}
	var reset *time.Time
	if ms, err := usage.BillingCycleEnd.Int64(); err == nil && ms > 0 {
		at := time.UnixMilli(ms).UTC()
		reset = &at
	}
	var windows []base.LimitWindow
	add := func(id, label string, used *float64) {
		if used == nil {
			return
		}
		percent := min(max(*used, 0), 100)
		remaining := 100 - percent
		windows = append(windows, base.LimitWindow{ID: id, Label: label, UsedPercent: &percent, RemainingPercent: &remaining, ResetAt: reset})
	}
	if plan := usage.PlanUsage; plan != nil {
		total := plan.TotalPercentUsed
		if total == nil && plan.Limit != nil && *plan.Limit > 0 && plan.Remaining != nil {
			percent := 100 * (1 - *plan.Remaining / *plan.Limit)
			total = &percent
		}
		add("cursor/included", "Included usage", total)
		add("cursor/auto", "Auto models", plan.AutoPercentUsed)
		add("cursor/api", "API models", plan.APIPercentUsed)
	}
	if spend := usage.SpendLimitUsage; spend != nil && spend.IndividualLimit != nil && *spend.IndividualLimit > 0 && spend.IndividualRemaining != nil {
		used := 100 * (1 - *spend.IndividualRemaining / *spend.IndividualLimit)
		add("cursor/on-demand", "On-demand spending limit", &used)
	}
	return windows
}
