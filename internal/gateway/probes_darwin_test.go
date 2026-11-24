package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olegpustovit/agentpack/internal/harness"
)

func TestMacOSReadOnlyAgentViewsDeferCLIProbes(t *testing.T) {
	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	claude := filepath.Join(dir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\ntouch \"$AP_TEST_PROBE_MARKER\"\nprintf '%s\\n' '{\"loggedIn\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("AP_TEST_PROBE_MARKER", ran)
	for _, target := range harness.AllTargets() {
		override, _ := target.Binary()
		t.Setenv(override, filepath.Join(dir, string(target)))
	}
	var cache agentAccounts
	accounts := cache.read(t.Context(), false)
	if !slices.ContainsFunc(accounts, func(account agentAccount) bool {
		return account.Harness == "claude" && account.Installed && account.CheckRequired
	}) {
		t.Fatalf("passive accounts = %+v", accounts)
	}
	versions := agentVersions(t.Context(), false)
	if !versions[0].Installed || versions[0].Version != "" {
		t.Fatalf("passive versions = %+v", versions)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("an agent CLI started during a passive read")
	}
	accounts = cache.read(t.Context(), true)
	if !slices.ContainsFunc(accounts, func(account agentAccount) bool {
		return account.Harness == "claude" && account.SignedIn && !account.CheckRequired
	}) {
		t.Fatalf("explicit account check = %+v", accounts)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("explicit check did not run:", err)
	}
}

func TestMacOSLimitsProbesRequireAnExplicitRefresh(t *testing.T) {
	g, origin := identityGateway(t, t.TempDir(), nil)
	handler := g.Handler()
	probes := make(chan struct{}, 1)
	g.limits.probe = func(context.Context) []harness.LimitSnapshot {
		probes <- struct{}{}
		return []harness.LimitSnapshot{{Harness: "claude"}}
	}
	request := func(method, path string) {
		t.Helper()
		r := httptest.NewRequest(method, origin.String()+path, nil)
		r.Header.Set("Authorization", "Bearer "+g.local.Secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	request(http.MethodGet, "/api/limits")
	request(http.MethodPost, "/api/limits/refresh")
	g.operations.Wait()
	if len(probes) != 0 {
		t.Fatal("a passive request started a limits probe")
	}
	request(http.MethodPost, "/api/limits/refresh?explicit=1")
	if len(probes) != 1 {
		t.Fatal("explicit refresh did not start a limits probe")
	}
}
