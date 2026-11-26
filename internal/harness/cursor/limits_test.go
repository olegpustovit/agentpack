package cursor

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadLimitsUsesCursorLoginAndSanitizesErrors(t *testing.T) {
	t.Setenv("CURSOR_AUTH_TOKEN", "fixture-token")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	status := http.StatusOK
	http.DefaultTransport = usageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatalf("unexpected usage request")
		}
		body := `{"enabled":true,"billingCycleEnd":"1795861461000","planUsage":{"totalPercentUsed":28,"autoPercentUsed":40,"apiPercentUsed":0},"spendLimitUsage":{"individualLimit":100,"individualRemaining":100}}`
		if status != http.StatusOK {
			body = "private provider error fixture-token"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	got := ReadLimits(context.Background())
	if got.State != "available" || got.Harness != "cursor" || len(got.Windows) != 4 || *got.Windows[0].RemainingPercent != 72 || *got.Windows[2].UsedPercent != 0 || got.Windows[0].ResetAt == nil {
		t.Fatalf("snapshot=%+v", got)
	}
	status = http.StatusUnauthorized
	got = ReadLimits(context.Background())
	if got.State != "error" || strings.Contains(got.Error, "fixture-token") || !strings.Contains(got.Error, "login") {
		t.Fatalf("error=%+v", got)
	}
}

func TestUsageWindowsDoesNotInventMissingPercentages(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		count     int
		remaining float64
	}{
		{`{"enabled":true,"planUsage":{}}`, 0, 0},
		{`{"enabled":true,"planUsage":{"limit":2000,"remaining":735}}`, 1, 36.75},
		{`{"enabled":false,"planUsage":{"totalPercentUsed":20}}`, 0, 0},
		{`{"enabled":true,"planUsage":{"limit":0,"remaining":0}}`, 0, 0},
	} {
		var usage currentPeriodUsage
		if err := json.Unmarshal([]byte(tc.raw), &usage); err != nil {
			t.Fatal(err)
		}
		windows := usageWindows(usage)
		if len(windows) != tc.count {
			t.Fatalf("%s: windows=%+v", tc.raw, windows)
		}
		if tc.count > 0 && math.Abs(*windows[0].RemainingPercent-tc.remaining) > 0.000001 {
			t.Fatalf("remaining=%v", *windows[0].RemainingPercent)
		}
	}
}

func TestUsageTokenReadsNativeCLIStore(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native keychain")
	}
	t.Setenv("CURSOR_AUTH_TOKEN", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	dir := filepath.Join(root, "cursor")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(root, "Cursor")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"accessToken":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := usageToken()
	if err != nil || token != "fixture" {
		t.Fatalf("native token lookup failed: %v", err)
	}
}

func TestReadLimitsWithoutLoginAsksForSignInOnlyWithTheCLI(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(name, root)
	}
	t.Setenv("CURSOR_AUTH_TOKEN", "")
	t.Setenv("AGENT_CLI_CREDENTIAL_STORE", "")

	t.Setenv("CURSOR_AGENT_PATH", filepath.Join(root, "missing"))
	if got := ReadLimits(context.Background()); got.State != "unsupported" || got.Error != "cursor CLI not found" {
		t.Fatalf("without the CLI: %+v", got)
	}
	t.Setenv("CURSOR_AGENT_PATH", os.Args[0])
	want := "No Cursor sign-in on this machine. Sign in from Vault."
	if runtime.GOOS == "darwin" {
		want = "Cursor's keychain login can't be read. Sign in once from Vault to show limits."
	}
	if got := ReadLimits(context.Background()); got.State != "error" || got.Error != want {
		t.Fatalf("with the CLI: %+v", got)
	}
}
