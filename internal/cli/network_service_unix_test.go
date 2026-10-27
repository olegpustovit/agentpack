//go:build linux

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/network"
)

func TestServiceInstallStopAndUninstallWithNativeCommand(t *testing.T) {
	directory := t.TempDir()
	home := filepath.Join(directory, "data")
	config := filepath.Join(directory, "config")
	commands := filepath.Join(directory, "commands")
	t.Setenv("AGENTPACK_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AGENTPACK_SERVICE_TEST_LOG", commands)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(directory, "systemctl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$AGENTPACK_SERVICE_TEST_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := Runner{Stdout: &output, Stderr: &output}
	for _, action := range []string{"install", "stop", "start", "uninstall"} {
		code, err := runner.runNetworkService(context.Background(), []string{action})
		if err != nil || code != 0 {
			t.Fatalf("%s: %d %v %s", action, code, err, output.String())
		}
		enabled, err := network.GatewayEnabled(home)
		if err != nil {
			t.Fatal(err)
		}
		if enabled != (action == "install" || action == "start") {
			t.Fatalf("%s enabled=%v", action, enabled)
		}
	}
	if _, err := os.Stat(filepath.Join(config, "systemd", "user", gatewayServiceName)); !os.IsNotExist(err) {
		t.Fatal("uninstall retained unit", err)
	}
	data, err := os.ReadFile(commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"--user daemon-reload", "--user enable agentpack-gateway.service", "--user stop agentpack-gateway.service", "--user restart agentpack-gateway.service", "--user disable --now agentpack-gateway.service"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("missing %s", expected)
		}
	}
}
