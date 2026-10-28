package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/harness/claude"
	"github.com/olegpustovit/agentpack/internal/harness/codex"
	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/manifest"
	"github.com/olegpustovit/agentpack/internal/mode"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/registry"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
	"github.com/pelletier/go-toml/v2"
)

func TestRestoredExternalDefinitionsKeepNativeStateAndIsolateModes(t *testing.T) {
	userHome, agentpackHome, stagingRoot := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	t.Setenv("AGENTPACK_HOME", agentpackHome)
	t.Setenv("AGENTPACK_STAGING_ROOT", stagingRoot)
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "")
	nativeCodex := filepath.Join(userHome, ".codex")
	if err := os.MkdirAll(nativeCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	const auth = `{"OPENAI_API_KEY":"native-fixture-secret"}`
	if err := os.WriteFile(filepath.Join(nativeCodex, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := registry.New(agentpackHome)
	if err != nil {
		t.Fatal(err)
	}
	type destinationInfo struct {
		definition string
		workspace  string
		packID     string
		revisionID string
	}
	destinations := make([]destinationInfo, 2)
	for i := range destinations {
		definition, workspace := t.TempDir(), t.TempDir()
		if err := manifest.WriteStub(definition, "external-definition", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		if err := manifest.ReplaceModes(definition, map[string]mode.Definition{"review": {Base: mode.BaseAll}}); err != nil {
			t.Fatal(err)
		}
		server := fmt.Sprintf("project-%c", 'a'+i)
		file, err := os.OpenFile(paths.ManifestPath(definition), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(file, "\n[mcp.servers.%s]\ncommand = %q\n", server, "fixture-"+server); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		lock := lockfile.EmptyForProject(definition)
		if err := lock.Save(definition); err != nil {
			t.Fatal(err)
		}
		pack, revision, err := store.Capture(registry.CaptureInput{Name: "External", Project: definition, Manifest: mustRead(t, paths.ManifestPath(definition)), Lock: mustRead(t, paths.LockPath(definition))})
		if err != nil {
			t.Fatal(err)
		}
		destinations[i] = destinationInfo{definition: definition, workspace: workspace, packID: pack.ID, revisionID: revision.ID}
	}

	type contextKey struct{ project, mode string }
	contexts := []contextKey{{"project-a", "default"}, {"project-a", "review"}, {"project-b", "default"}, {"project-b", "review"}}
	service := packSync.NewService()
	var group sync.WaitGroup
	failures := make(chan error, len(destinations))
	for _, destination := range destinations {
		group.Add(1)
		go func(destination destinationInfo) {
			defer group.Done()
			for _, modeName := range []string{"default", "review"} {
				if err := store.Restore(destination.packID, destination.revisionID, destination.definition, func() error {
					for _, target := range []base.Target{base.Claude, base.Codex} {
						if err := service.RestoreFrozenOptions(context.Background(), destination.definition, packSync.SyncOptions{Mode: modeName, Target: &target, WorkspaceRoot: destination.workspace}); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					failures <- err
					return
				}
			}
		}(destination)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}

	stagedHomes := map[string]bool{}
	for index, key := range contexts {
		destination := destinations[index/2]
		home, err := codex.CurrentHome(destination.definition, key.mode)
		if err != nil {
			t.Fatal(err)
		}
		if stagedHomes[home] {
			t.Fatalf("Codex staging home shared across %s/%s", key.project, key.mode)
		}
		stagedHomes[home] = true
		if !base.DurablePathMatches(filepath.Join(home, "auth.json"), filepath.Join(nativeCodex, "auth.json")) {
			t.Fatalf("Codex auth is not durable for %s/%s", key.project, key.mode)
		}
		config := mustRead(t, filepath.Join(home, "config.toml"))
		var codexConfig map[string]any
		if err := toml.Unmarshal(config, &codexConfig); err != nil {
			t.Fatal(err)
		}
		servers, ok := codexConfig["mcp_servers"].(map[string]any)
		if !ok || servers[key.project] == nil || len(servers) != 1 {
			t.Fatalf("Codex settings leaked across %s/%s: %#v", key.project, key.mode, codexConfig["mcp_servers"])
		}
		history := filepath.Join(home, "sessions", "continuity.jsonl")
		if err := os.MkdirAll(filepath.Dir(history), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(history, []byte(key.project+"/"+key.mode), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(nativeCodex, "sessions", "continuity.jsonl")); err != nil || string(got) != key.project+"/"+key.mode {
			t.Fatalf("Codex history not durable for %s/%s: %q %v", key.project, key.mode, got, err)
		}

		stageMode := mode.ImplicitEffective()
		if key.mode != "default" {
			stageMode, err = mode.NewEffective(key.mode, mode.Definition{Base: mode.BaseAll}, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		stage := base.StageContext{ProjectRoot: destination.definition, WorkspaceRoot: destination.workspace, Mode: stageMode}
		settingPath, err := claude.SettingsPath(stage)
		if err != nil {
			t.Fatal(err)
		}
		if err := claude.MaterializeSettings(stage); err != nil {
			t.Fatal(err)
		}
		if err := claude.SetMCPAllowlist(stage, []string{key.project + "-" + key.mode}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(settingPath)
		if err != nil {
			t.Fatal(err)
		}
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		claudeServers := settings["enabledMcpjsonServers"].([]any)
		if len(claudeServers) != 1 || claudeServers[0] != key.project+"-"+key.mode {
			t.Fatalf("Claude settings leaked across %s/%s: %#v", key.project, key.mode, claudeServers)
		}
	}

	for _, destination := range destinations {
		entries, err := os.ReadDir(destination.workspace)
		if err != nil || len(entries) != 0 {
			t.Fatalf("external restore modified workspace %s: %v %v", destination.workspace, entries, err)
		}
	}
	first := destinations[0]
	target := base.Codex
	if err := service.RestoreFrozenOptions(context.Background(), first.definition, packSync.SyncOptions{Mode: "default", Target: &target, WorkspaceRoot: first.workspace}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_PATH", os.Args[0])
	var probeOutput bytes.Buffer
	runner := Runner{Service: service, Stdout: &probeOutput}
	code, err := runner.runProbe(context.Background(), first.definition, first.workspace, "default", []string{"--agent", "codex", "--strict-external", "--json"}, false)
	if code != 5 || err == nil || !strings.Contains(probeOutput.String(), "PROBE_FAILED") {
		t.Fatalf("native probe failure was not reported: code=%d err=%v output=%s", code, err, probeOutput.String())
	}
	if strings.Contains(probeOutput.String(), "native-fixture-secret") {
		t.Fatal("probe report exposed native credentials")
	}
}

func TestRestoreDuringLiveLaunchKeepsOriginalGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("AGENTPACK_HOME", t.TempDir())
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	definition, workspace := t.TempDir(), t.TempDir()
	if err := manifest.WriteStub(definition, "launch-snapshot", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	lock := lockfile.EmptyForProject(definition)
	if err := lock.Save(definition); err != nil {
		t.Fatal(err)
	}
	store, err := registry.New(os.Getenv("AGENTPACK_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	pack, first, err := store.Capture(registry.CaptureInput{Name: "Launch snapshot", Project: definition, Manifest: mustRead(t, paths.ManifestPath(definition)), Lock: mustRead(t, paths.LockPath(definition))})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.Capture(registry.CaptureInput{ID: pack.ID, Project: definition, Manifest: []byte("name = \"restored-during-launch\"\n"), Lock: mustRead(t, paths.LockPath(definition))})
	if err != nil || second.ID == first.ID {
		t.Fatalf("make second revision: %#v %v", second, err)
	}
	service := packSync.NewService()
	if err := store.Restore(pack.ID, first.ID, definition, func() error {
		return service.RestoreFrozenOptions(context.Background(), definition, packSync.SyncOptions{Mode: "default", Target: codexTarget(), WorkspaceRoot: workspace})
	}); err != nil {
		t.Fatal(err)
	}
	oldHome, err := codex.CurrentHome(definition, "default")
	if err != nil {
		t.Fatal(err)
	}
	started, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
	t.Setenv("CODEX_PATH", os.Args[0])
	command, err := codex.New().LaunchCommand(base.LaunchContext{ProjectRoot: definition, Mode: mode.ImplicitEffective()})
	if err != nil {
		t.Fatal(err)
	}
	launchHome := ""
	for _, entry := range command.Env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "CODEX_HOME" {
			launchHome = value
		}
	}
	if launchHome == "" {
		t.Fatal("Codex launch did not select its generation")
	}
	marker := filepath.Join(launchHome, "launch-generation.marker")
	if err := os.WriteFile(marker, []byte("first-generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	command.Args = []string{os.Args[0], "-test.run=^TestCodexContinuityLaunchHelper$"}
	command.Env = append(command.Env, "AGENTPACK_CONTINUITY_LAUNCH_HELPER=1", "AGENTPACK_CONTINUITY_STARTED="+started, "AGENTPACK_CONTINUITY_RELEASE="+release, "AGENTPACK_CONTINUITY_HOME="+launchHome)
	var childOutput bytes.Buffer
	command.Stdout, command.Stderr = &childOutput, &childOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = os.WriteFile(release, nil, 0o600)
			_ = command.Wait()
			_ = codex.New().AfterLaunch(base.LaunchContext{ProjectRoot: definition, Mode: mode.ImplicitEffective(), Command: command})
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("launch helper did not start: %s", childOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := store.Restore(pack.ID, second.ID, definition, func() error {
		return service.RestoreFrozenOptions(context.Background(), definition, packSync.SyncOptions{Mode: "default", Target: codexTarget(), WorkspaceRoot: workspace})
	}); err != nil {
		t.Fatal(err)
	}
	newHome, err := codex.CurrentHome(definition, "default")
	if err != nil || newHome == oldHome {
		t.Fatalf("restore did not publish a new generation: old=%s new=%s err=%v", oldHome, newHome, err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("launch did not finish against original generation: %v %s", err, childOutput.String())
	}
	finished = true
	if err := codex.New().AfterLaunch(base.LaunchContext{ProjectRoot: definition, Mode: mode.ImplicitEffective(), Command: command}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexContinuityLaunchHelper(t *testing.T) {
	if os.Getenv("AGENTPACK_CONTINUITY_LAUNCH_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("AGENTPACK_CONTINUITY_STARTED"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("AGENTPACK_CONTINUITY_RELEASE")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launch helper timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, want := os.Getenv("CODEX_HOME"), os.Getenv("AGENTPACK_CONTINUITY_HOME"); got != want {
		t.Fatalf("Codex launch home changed: got %q want %q", got, want)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "launch-generation.marker"))
	if err != nil || string(data) != "first-generation" {
		t.Fatalf("launch generation changed: %q %v", data, err)
	}
}

func codexTarget() *base.Target {
	target := base.Codex
	return &target
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
