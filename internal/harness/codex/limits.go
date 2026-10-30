package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const (
	limitsSource        = "codex app-server account/rateLimits/read"
	limitsClientVersion = "agentpack"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type rateLimitSnapshot struct {
	LimitID   *string          `json:"limitId"`
	LimitName *string          `json:"limitName"`
	PlanType  *string          `json:"planType"`
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

type rateLimitsResponse struct {
	RateLimits          *rateLimitSnapshot           `json:"rateLimits"`
	RateLimitsByLimitID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
}

// ReadLimits asks the authenticated Codex app-server for account rate limits. It uses the
// user's existing login, including Agentpack's shared auth store, and sends no inference turns.
func ReadLimits(ctx context.Context, codexHome string) base.LimitSnapshot {
	snapshot := base.LimitSnapshot{Harness: "codex", State: base.LimitStateError, Source: limitsSource, ObservedAt: time.Now().UTC()}
	exe, err := base.LookPathEnv("CODEX_PATH", "codex")
	if err != nil {
		snapshot.State = base.LimitStateUnsupported
		snapshot.Error = "codex CLI not found"
		return snapshot
	}
	if codexHome == "" && os.Getenv("CODEX_HOME") == "" {
		var err error
		codexHome, err = os.MkdirTemp("", "agentpack-codex-limits-")
		if err != nil {
			snapshot.Error = "create limits home: " + err.Error()
			return snapshot
		}
		defer os.RemoveAll(codexHome)
		native, ok := nativeHome()
		if !ok {
			snapshot.Error = "cannot locate Codex credentials"
			return snapshot
		}
		if err := prepareAuth(native, codexHome); err != nil {
			snapshot.Error = "prepare limits credentials: " + err.Error()
			return snapshot
		}
		if err := forceAuthFileStore(codexHome); err != nil {
			snapshot.Error = "configure limits credentials: " + err.Error()
			return snapshot
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "app-server")
	cmd.Env = withCodexHome(os.Environ(), codexHome)
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
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"agentpack","version":"` + limitsClientVersion + `"}}}`,
		`{"method":"initialized"}`,
		`{"id":2,"method":"account/rateLimits/read","params":null}`,
	}
	for _, request := range requests {
		if _, err := io.WriteString(stdin, request+"\n"); err != nil {
			snapshot.Error = err.Error()
			return snapshot
		}
	}

	result, err := awaitResponse(ctx, stdout, "2")
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	var usage rateLimitsResponse
	if err := json.Unmarshal(result, &usage); err != nil {
		snapshot.Error = "unexpected rate-limit response: " + err.Error()
		return snapshot
	}
	snapshot.Windows, snapshot.Plan = limitWindows(usage)
	if len(snapshot.Windows) == 0 {
		snapshot.Error = "codex reported no rate-limit windows"
		return snapshot
	}
	snapshot.State = base.LimitStateAvailable
	return snapshot
}

func awaitResponse(ctx context.Context, stdout io.Reader, id string) (json.RawMessage, error) {
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for lines.Scan() {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codex app-server timed out")
		}
		var message rpcMessage
		if err := json.Unmarshal(bytes.TrimSpace(lines.Bytes()), &message); err != nil || len(message.ID) == 0 {
			continue
		}
		if strings.Trim(string(message.ID), `"`) != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("codex app-server: %s", message.Error.Message)
		}
		return message.Result, nil
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("codex app-server closed before responding")
}

func limitWindows(usage rateLimitsResponse) ([]base.LimitWindow, string) {
	buckets := usage.RateLimitsByLimitID
	if len(buckets) == 0 && usage.RateLimits != nil {
		buckets = map[string]rateLimitSnapshot{"codex": *usage.RateLimits}
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var windows []base.LimitWindow
	plan := ""
	for _, key := range keys {
		bucket := buckets[key]
		if plan == "" && bucket.PlanType != nil {
			plan = *bucket.PlanType
		}
		name := key
		if bucket.LimitName != nil && *bucket.LimitName != "" {
			name = *bucket.LimitName
		}
		for _, slot := range []struct {
			id     string
			window *rateLimitWindow
		}{{"primary", bucket.Primary}, {"secondary", bucket.Secondary}} {
			if slot.window == nil {
				continue
			}
			windows = append(windows, windowFrom(key, slot.id, name, slot.window))
		}
	}
	return windows, plan
}

func windowFrom(bucket, slot, name string, window *rateLimitWindow) base.LimitWindow {
	used := clampPercent(window.UsedPercent)
	remaining := 100 - used
	result := base.LimitWindow{
		ID:               bucket + "/" + slot,
		Label:            name + " " + slot,
		UsedPercent:      &used,
		RemainingPercent: &remaining,
	}
	if window.WindowDurationMins != nil {
		seconds := *window.WindowDurationMins * 60
		result.DurationSeconds = &seconds
		result.Label = name + " " + durationLabel(seconds)
	}
	if window.ResetsAt != nil {
		resetAt := time.Unix(*window.ResetsAt, 0).UTC()
		result.ResetAt = &resetAt
	}
	return result
}

func clampPercent(value float64) float64 {
	return min(max(value, 0), 100)
}

func durationLabel(seconds int64) string {
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dm", seconds/60)
	}
}
