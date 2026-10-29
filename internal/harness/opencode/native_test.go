package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/mode"
)

// Set the paths to released binaries to exercise their real configuration loaders.
func TestNativeYoloIsLaunchScoped(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	for _, test := range []struct {
		name, env string
		args      []string
	}{
		{"v1", "AGENTPACK_TEST_OPENCODE_V1", []string{"debug", "config", "--pure"}},
		{"v2", "AGENTPACK_TEST_OPENCODE_V2", []string{"debug", "config", "--standalone"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := os.Getenv(test.env)
			if binary == "" {
				t.Skipf("set %s to a released OpenCode binary", test.env)
			}
			home, project := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for _, name := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
				t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
			}
			t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
			t.Setenv("OPENCODE_PATH", binary)
			content := `{"username":"inline-fixture","permission":"deny"}`
			if test.name == "v2" {
				content = `{"username":"inline-fixture","permission":"deny","permissions":[{"action":"*","resource":"*","effect":"deny"}]}`
			}
			t.Setenv("OPENCODE_CONFIG_CONTENT", content)
			t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
			ctx := base.LaunchContext{ProjectRoot: project, Mode: mode.ImplicitEffective(), Arguments: test.args}
			root, err := stagedRoot(base.StageContext{ProjectRoot: project, Mode: ctx.Mode})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(packConfigPath(root), []byte(`{"permission":"ask"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, yolo := range []bool{true, false} {
				ctx.Yolo = yolo
				cmd, err := launch(ctx)
				if err != nil {
					t.Fatal(err)
				}
				cmd.Dir = project
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("native config (yolo=%t): %v\n%s", yolo, err, output)
				}
				var value any
				if err := json.Unmarshal(output, &value); err != nil {
					t.Fatalf("invalid native config: %v\n%s", err, output)
				}
				want := "deny"
				if yolo {
					want = "allow"
				}
				if test.name == "v1" {
					config := value.(map[string]any)
					permissions, ok := config["permission"].(map[string]any)
					if !ok || permissions["*"] != want || config["username"] != "inline-fixture" {
						t.Fatalf("native v1 config: %s", output)
					}
				} else {
					entries := value.([]any)
					last := entries[len(entries)-1].(map[string]any)["info"].(map[string]any)
					permissions := last["permissions"].([]any)
					if permissions[len(permissions)-1].(map[string]any)["effect"] != want || last["username"] != "inline-fixture" {
						t.Fatalf("native v2 config: %s", output)
					}
				}
			}
		})
	}
}

func TestNativeAttributionWithCustomDirectory(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	for _, test := range []struct {
		name, env string
		args      []string
	}{
		{"v1", "AGENTPACK_TEST_OPENCODE_V1", []string{"debug", "config", "--pure"}},
		{"v2", "AGENTPACK_TEST_OPENCODE_V2", []string{"debug", "config", "--standalone"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := os.Getenv(test.env)
			if binary == "" {
				t.Skipf("set %s to a released OpenCode binary", test.env)
			}
			home, project, custom := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for _, name := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
				t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
			}
			t.Setenv("AGENTPACK_HOME", t.TempDir())
			t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
			t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "")
			t.Setenv("OPENCODE_PATH", binary)
			t.Setenv("OPENCODE_CONFIG_DIR", custom)
			t.Setenv("OPENCODE_CONFIG_CONTENT", "")
			t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
			stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
			if err := prepare(stage); err != nil {
				t.Fatal(err)
			}
			if err := finalizeSources(nil, stage); err != nil {
				t.Fatal(err)
			}
			root, err := stagedRoot(stage)
			if err != nil {
				t.Fatal(err)
			}
			command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: test.args})
			if err != nil {
				t.Fatal(err)
			}
			command.Dir = project
			output, err := command.CombinedOutput()
			if err != nil || !bytes.Contains(output, []byte(instructionsFile)) {
				t.Fatalf("native attribution: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, instructionsFile)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeV2LoadsGeneratedHookBridge(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	if runtime.GOOS == "windows" {
		t.Skip("fixture hook executable uses a shell script")
	}
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V2")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V2 to a released OpenCode 2 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_SERVER_PASSWORD", "fixture-password")
	marker := filepath.Join(home, "hook-fired")
	t.Setenv("AGENTPACK_TEST_HOOK_MARKER", marker)
	fake := filepath.Join(home, "hook-runner")
	script := "#!/bin/sh\ncat >/dev/null\nprintf 'run\\n' >> \"$AGENTPACK_TEST_HOOK_MARKER\"\nprintf '%s\\n' '{\"decision\":\"allow\",\"additional_context\":\"fixture context\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(home, "CONFIG", "opencode", "skills", "user-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userSkill, []byte("---\nname: user-skill\ndescription: User fixture\n---\n\nUser.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	userHook := filepath.Join(home, "user-hook-fired")
	userHookJSON, _ := json.Marshal(userHook)
	userPlugin := filepath.Join(home, "CONFIG", "opencode", "plugins", "user-hook.ts")
	if err := os.MkdirAll(filepath.Dir(userPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	pluginSource := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export default {id:"user-hook-fixture", async setup(host) { await host.session.hook("prompt", async () => appendFileSync(%s, "run\n")); }};`, userHookJSON)
	if err := os.WriteFile(userPlugin, []byte(pluginSource), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	root, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	packSkill := filepath.Join(root, "skills", "pack-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(packSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packSkill, []byte("---\nname: pack-skill\ndescription: Pack fixture\n---\n\nPack.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{
		{Event: hooks.PreToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "true"}, Origin: hooks.Origin{PackageKey: "fixture", SourceFile: "fixture"}},
		{Event: hooks.UserPromptSubmit, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "true"}, Origin: hooks.Origin{PackageKey: "fixture", SourceFile: "fixture", EventIndex: 1}},
	}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: root, StagedPackages: map[string]string{"fixture": project}})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range output.Files {
		if filepath.Base(file.Path) == "config.json" {
			file.JSON.(map[string]any)["agentpack"] = fake
		}
	}
	if err := hooks.WriteRenderedFiles(output); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSources(nil, stage); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"serve", "--hostname", "127.0.0.1", "--port", port}})
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = project
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	active := false
	for time.Now().Before(deadline) {
		request, err := http.NewRequest("GET", "http://"+address+"/api/plugin?directory="+url.QueryEscape(project), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth("opencode", "fixture-password")
		response, err := client.Do(request)
		if err == nil {
			var result struct {
				Data []struct {
					ID    string `json:"id"`
					State struct {
						Status string `json:"status"`
					} `json:"state"`
				} `json:"data"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if decodeErr == nil {
				count := 0
				for _, plugin := range result.Data {
					if plugin.ID == "agentpack-hooks" && plugin.State.Status == "active" {
						count++
					}
				}
				if count == 1 {
					active = true
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !active {
		t.Fatal("generated bridge did not become active")
	}
	skillRequest, err := http.NewRequest("GET", "http://"+address+"/api/skill?directory="+url.QueryEscape(project), nil)
	if err != nil {
		t.Fatal(err)
	}
	skillRequest.SetBasicAuth("opencode", "fixture-password")
	skillResponse, err := client.Do(skillRequest)
	if err != nil {
		t.Fatal(err)
	}
	var skills struct {
		Data []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.NewDecoder(skillResponse.Body).Decode(&skills); err != nil {
		skillResponse.Body.Close()
		t.Fatal(err)
	}
	skillResponse.Body.Close()
	found := map[string]bool{}
	for _, skill := range skills.Data {
		if (skill.Name == "user-skill" && base.DurablePathMatches(skill.Path, userSkill)) || (skill.Name == "pack-skill" && base.DurablePathMatches(skill.Path, packSkill)) {
			found[skill.Name] = true
		}
	}
	if !found["user-skill"] || !found["pack-skill"] {
		t.Fatalf("native skills = %+v", skills.Data)
	}
	post := func(path, body string, value any) {
		t.Helper()
		request, err := http.NewRequest("POST", "http://"+address+path+"?directory="+url.QueryEscape(project), bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth("opencode", "fixture-password")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", path, response.Status)
		}
		if err := json.NewDecoder(response.Body).Decode(value); err != nil {
			t.Fatal(err)
		}
	}
	var session struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	post("/api/session", "{}", &session)
	var prompt struct {
		Data struct {
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		} `json:"data"`
	}
	post("/api/session/"+session.Data.ID+"/prompt", `{"text":"hello","resume":false}`, &prompt)
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "run\n" || prompt.Data.Payload.Text != "hello\n\nfixture context" {
		t.Fatalf("prompt hook: marker=%q, text=%q, err=%v", data, prompt.Data.Payload.Text, err)
	}
	data, err = os.ReadFile(userHook)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("native user prompt hook: %q, %v", data, err)
	}
}

func TestNativeV1UserPluginRunsOnce(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V1")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V1 to a released OpenCode 1 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, ".config", "opencode"))
	marker := filepath.Join(home, "plugin-runs")
	markerJSON, _ := json.Marshal(marker)
	plugin := filepath.Join(home, ".config", "opencode", "plugins", "sentinel.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o755); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export default {id:"sentinel", server: async () => { appendFileSync(%s, "run\n"); return {}; }};`, markerJSON)
	if err := os.WriteFile(plugin, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSources(nil, stage); err != nil {
		t.Fatal(err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "agent", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	commandWithTimeout := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	commandWithTimeout.Dir, commandWithTimeout.Env = project, command.Env
	if output, err := commandWithTimeout.CombinedOutput(); err != nil {
		t.Fatalf("native plugin: %v\n%s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(data), "run\n") != 1 {
		t.Fatalf("plugin initialization count: %q, %v", data, err)
	}
}

func TestNativeV1CustomDirectoryPluginKeepsItsOrigin(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V1")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V1 to a released OpenCode 1 binary")
	}
	home, project, custom := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", filepath.Join(t.TempDir(), "stage with spaces"))
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_CONFIG_DIR", custom)
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	marker := filepath.Join(home, "plugin-runs")
	markerJSON, _ := json.Marshal(marker)
	if err := os.MkdirAll(filepath.Join(custom, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export function record() { appendFileSync(%s, "run\n"); }`, markerJSON)
	if err := os.WriteFile(filepath.Join(custom, "helper.js"), []byte(helper), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `import { record } from "../helper.js"; export default {id:"custom-fixture", server: async () => {record(); return {};}};`
	if err := os.WriteFile(filepath.Join(custom, "plugins", "sentinel.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	declaredMarker := filepath.Join(home, "declared-runs")
	declaredJSON, _ := json.Marshal(declaredMarker)
	declared := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export default {id:"declared-fixture", server: async (_, options) => {appendFileSync(%s, options.flag + "\n"); return {};}};`, declaredJSON)
	if err := os.WriteFile(filepath.Join(custom, "declared.js"), []byte(declared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(custom), "user.txt"), []byte("custom-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const config = `{"$schema":"https://opencode.ai/config.json","username":"{file:../user.txt}","model":"custom/model","plugin":[["./declared.js",{"flag":"v1-option"}]],"mcp":{"shared":{"type":"local","command":["custom-server"]}}}`
	if err := os.WriteFile(filepath.Join(custom, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	const configC = `{"$schema":"https://opencode.ai/config.json","model":"custom-jsonc/model"}`
	if err := os.WriteFile(filepath.Join(custom, "opencode.jsonc"), []byte(configC), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(`{"$schema":"https://opencode.ai/config.json","model":"project/model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(custom, "skills", "custom-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: custom-skill\ndescription: A custom skill\n---\n\nFixture.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commandFile := filepath.Join(custom, "commands", "custom-review.md")
	if err := os.MkdirAll(filepath.Dir(commandFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commandFile, []byte("---\ndescription: Review a change\n---\n\nReview the fixture.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	stage.StagedRoots = map[base.Target]string{}
	pack := "pack-server"
	if err := writeMCP(mcp.Entries{"shared": {Server: mcp.Server{Command: &pack}}, "pack-only": {Server: mcp.Server{Command: &pack}}}, stage); err != nil {
		t.Fatal(err)
	}
	root, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	packSkill := filepath.Join(root, "skills", "pack-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(packSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packSkill, []byte("---\nname: pack-skill\ndescription: A pack skill\n---\n\nFixture.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ kind, name, content string }{
		{"agents", "pack-agent", "---\nname: pack-agent\ndescription: A pack agent\n---\n\nAgent fixture.\n"},
		{"commands", "pack-command", "---\ndescription: A pack command\n---\n\nCommand fixture.\n"},
	} {
		dir := filepath.Join(root, item.kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, item.name+".md"), []byte(item.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := injectGuidance("Pack guidance fixture.", stage); err != nil {
		t.Fatal(err)
	}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{{Event: hooks.PreToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "true"}, Origin: hooks.Origin{PackageKey: "fixture", SourceFile: "fixture"}}}}
	rendered, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: root, StagedPackages: map[string]string{"fixture": project}})
	if err != nil {
		t.Fatal(err)
	}
	bridgeMarker := filepath.Join(home, "bridge-runs")
	bridgeJSON, _ := json.Marshal(bridgeMarker)
	for index := range rendered.Files {
		if filepath.Base(rendered.Files[index].Path) == "index.js" {
			rendered.Files[index].Text += fmt.Sprintf("\nimport { appendFileSync } from 'node:fs'; appendFileSync(%s, 'run\\n');\n", bridgeJSON)
		}
	}
	if err := hooks.WriteRenderedFiles(rendered); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSources(nil, stage); err != nil {
		t.Fatal(err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "agent", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	commandWithTimeout := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	commandWithTimeout.Dir, commandWithTimeout.Env = project, command.Env
	if output, err := commandWithTimeout.CombinedOutput(); err != nil {
		t.Fatalf("native custom plugin: %v\n%s", err, output)
	}
	bridgeData, err := os.ReadFile(bridgeMarker)
	if err != nil || string(bridgeData) != "run\n" {
		t.Fatalf("generated bridge initialization: %q, %v", bridgeData, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("custom plugin initialization: %q, %v", data, err)
	}
	data, err = os.ReadFile(declaredMarker)
	if err != nil || string(data) != "v1-option\n" {
		t.Fatalf("config-declared plugin initialization: %q, %v", data, err)
	}
	skillCommand, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "skill"}})
	if err != nil {
		t.Fatal(err)
	}
	skillCommand.Dir = project
	output, err := skillCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("custom skill discovery: %v\n%s", err, output)
	}
	listed, err := parseSkillList(string(output))
	if err != nil {
		t.Fatal(err)
	}
	found, foundPack := false, false
	for _, item := range listed {
		if item.Name == "pack-skill" {
			foundPack = true
		}
		if item.Name == "custom-skill" {
			if !base.DurablePathMatches(item.Location, skill) {
				t.Fatalf("custom skill origin = %s", item.Location)
			}
			found = true
		}
	}
	if !found || !foundPack {
		t.Fatalf("custom and pack skills discovered: custom=%v pack=%v", found, foundPack)
	}
	packCommand, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "config"}})
	if err != nil {
		t.Fatal(err)
	}
	packCommand.Dir = project
	packOutput, err := packCommand.CombinedOutput()
	if err != nil || !bytes.Contains(packOutput, []byte("pack-agent")) || !bytes.Contains(packOutput, []byte("Agent fixture.")) || !bytes.Contains(packOutput, []byte("pack-command")) || !bytes.Contains(packOutput, []byte("Command fixture.")) {
		t.Fatalf("pack agent and command discovery: %v\n%s", err, packOutput)
	}
	configCommand, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "config", "--pure"}})
	if err != nil {
		t.Fatal(err)
	}
	configCommand.Dir = project
	configOutput, err := configCommand.CombinedOutput()
	if err != nil || !bytes.Contains(configOutput, []byte("custom-review")) || !bytes.Contains(configOutput, []byte("Review the fixture.")) {
		t.Fatalf("custom command discovery: %v\n%s", err, configOutput)
	}
	var resolved map[string]any
	if err := json.Unmarshal(configOutput, &resolved); err != nil || resolved["model"] != "custom-jsonc/model" || resolved["username"] != "custom-user" {
		t.Fatalf("custom config: %v\n%s", err, configOutput)
	}
	foundGuidance := false
	for _, instruction := range resolved["instructions"].([]any) {
		if name, ok := instruction.(string); ok && filepath.Clean(name) == filepath.Join(root, "AGENTS.md") {
			foundGuidance = true
		}
	}
	if !foundGuidance {
		t.Fatalf("pack guidance missing from instructions: %s", configOutput)
	}
	servers, ok := resolved["mcp"].(map[string]any)
	if !ok || servers["shared"].(map[string]any)["command"].([]any)[0] != "custom-server" || servers["pack-only"] == nil {
		t.Fatalf("custom MCP config: %s", configOutput)
	}
	t.Setenv("AGENTPACK_INLINE_AUTOUPDATE", "false")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"inline/model","autoupdate":{env:AGENTPACK_INLINE_AUTOUPDATE},"mcp":{"inline-only":{"type":"local","command":["inline-server"]}}}`)
	inline, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "config", "--pure"}})
	if err != nil {
		t.Fatal(err)
	}
	inline.Dir = project
	inlineOutput, err := inline.CombinedOutput()
	if err != nil {
		t.Fatalf("inline config: %v\n%s", err, inlineOutput)
	}
	var inlineResolved map[string]any
	if err := json.Unmarshal(inlineOutput, &inlineResolved); err != nil || inlineResolved["model"] != "inline/model" || inlineResolved["autoupdate"] != false {
		t.Fatalf("inline precedence: %v\n%s", err, inlineOutput)
	}
	inlineServers, ok := inlineResolved["mcp"].(map[string]any)
	if !ok || inlineServers["inline-only"] == nil || inlineServers["pack-only"] == nil || inlineServers["shared"] == nil {
		t.Fatalf("inline servers: %s", inlineOutput)
	}
	data, err = os.ReadFile(filepath.Join(custom, "opencode.json"))
	if err != nil || string(data) != config {
		t.Fatalf("custom config changed: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(custom, "opencode.jsonc"))
	if err != nil || string(data) != configC {
		t.Fatalf("custom JSONC changed: %v", err)
	}
}

func TestNativeV1UserSkillRemainsAtItsOrigin(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V1")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V1 to a released OpenCode 1 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	skill := filepath.Join(home, ".config", "opencode", "skills", "personal-fixture", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: personal-fixture\ndescription: A user skill\n---\n\nFixture.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	root, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "personal-fixture")); !os.IsNotExist(err) {
		t.Fatalf("user skill copied to staging: %v", err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "skill", "--pure"}})
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = project
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native skill discovery: %v\n%s", err, output)
	}
	listed, err := parseSkillList(string(output))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range listed {
		if item.Name == "personal-fixture" {
			count++
			if !base.DurablePathMatches(item.Location, skill) {
				t.Fatalf("skill origin = %s, want %s", item.Location, skill)
			}
		}
	}
	if count != 1 {
		t.Fatalf("user skill appeared %d times", count)
	}
}

func TestNativeV1ProjectModelOverridesGlobalConfig(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V1")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V1 to a released OpenCode 1 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	for _, name := range []string{"DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	t.Setenv("AGENTPACK_TEST_SERVER_NAME", "shared")
	global := filepath.Join(home, "xdg-config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	const userConfig = "{ // global settings\n\"$schema\":\"https://opencode.ai/config.json\",\"model\":\"global/model\",\"username\":\"global-fixture\",\"mcp\":{\"{env:AGENTPACK_TEST_SERVER_NAME}\":{\"type\":\"local\",\"command\":[\"{file:server.txt}\"],\"environment\":{\"TOKEN\":\"fixture-secret\"}}},\"lsp\":{\"gopls\":{\"command\":[\"user-gopls\"],\"extensions\":[\".go\"]}},}\n"
	if err := os.WriteFile(global, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(global), "server.txt"), []byte("user-server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(`{"$schema":"https://opencode.ai/config.json","model":"project/model","mcp":{"shared":{"type":"local","command":["project-server"]}},"lsp":{"gopls":{"command":["project-gopls"],"extensions":[".go"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	stage.StagedRoots = map[base.Target]string{}
	packCommand := "pack-server"
	entries := mcp.Entries{"shared": {Server: mcp.Server{Command: &packCommand}}, "pack-only": {Server: mcp.Server{Command: &packCommand}}}
	if err := writeMCP(entries, stage); err != nil {
		t.Fatal(err)
	}
	lsp := map[string]json.RawMessage{"gopls": json.RawMessage(`{"command":"pack-gopls","extensionToLanguage":{".go":"go"}}`), "pack-lsp": json.RawMessage(`{"command":"pack-lsp","extensionToLanguage":{".test":"test"}}`)}
	if err := writeLSP(lsp, stage); err != nil {
		t.Fatal(err)
	}
	staged, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	stagedConfig, err := os.ReadFile(filepath.Join(staged, "opencode.json"))
	if err != nil || bytes.Contains(stagedConfig, []byte("fixture-secret")) || bytes.Contains(stagedConfig, []byte("user-server")) || bytes.Contains(stagedConfig, []byte("user-gopls")) {
		t.Fatalf("user server copied into staging: %v", err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "config", "--pure"}})
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = project
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native config: %v\n%s", err, output)
	}
	var config map[string]any
	if err := json.Unmarshal(output, &config); err != nil || config["model"] != "project/model" || config["username"] != "global-fixture" {
		t.Fatalf("native config = %s, %v", output, err)
	}
	servers, ok := config["mcp"].(map[string]any)
	if !ok || servers["shared"].(map[string]any)["command"].([]any)[0] != "project-server" || servers["pack-only"] == nil {
		t.Fatalf("native MCP config = %s", output)
	}
	languageServers, ok := config["lsp"].(map[string]any)
	if !ok || languageServers["gopls"].(map[string]any)["command"].([]any)[0] != "project-gopls" || languageServers["pack-lsp"] == nil {
		t.Fatalf("native LSP config = %s", output)
	}
	data, err := os.ReadFile(global)
	if err != nil || string(data) != userConfig {
		t.Fatalf("user config modified: %q, %v", data, err)
	}
}

func TestNativeV1LoadsGeneratedHookBridge(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V1")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V1 to a released OpenCode 1 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", t.TempDir())
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_PATH", binary)
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	root, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{{Event: hooks.PreToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "true"}, Origin: hooks.Origin{PackageKey: "fixture", SourceFile: "fixture"}}}}
	output, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: root, StagedPackages: map[string]string{"fixture": project}})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "bridge-loaded")
	markerJSON, _ := json.Marshal(marker)
	for index := range output.Files {
		if filepath.Base(output.Files[index].Path) == "index.js" {
			output.Files[index].Text += fmt.Sprintf("\nimport { appendFileSync } from 'node:fs'; appendFileSync(%s, 'run\\n');\n", markerJSON)
		}
	}
	if err := hooks.WriteRenderedFiles(output); err != nil {
		t.Fatal(err)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "agent", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	commandWithTimeout := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	commandWithTimeout.Dir, commandWithTimeout.Env = project, command.Env
	if output, err := commandWithTimeout.CombinedOutput(); err != nil {
		t.Fatalf("native bridge: %v\n%s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("bridge load: %q, %v", data, err)
	}
}

func TestNativeV2PluginKeepsItsOrigin(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	binary := os.Getenv("AGENTPACK_TEST_OPENCODE_V2")
	if binary == "" {
		t.Skip("set AGENTPACK_TEST_OPENCODE_V2 to a released OpenCode 2 binary")
	}
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(home, name))
	}
	t.Setenv("AGENTPACK_STAGING_ROOT", filepath.Join(t.TempDir(), "stage with spaces"))
	t.Setenv("AGENTPACK_KEEP_ATTRIBUTION", "1")
	t.Setenv("OPENCODE_PATH", binary)
	custom := filepath.Join(home, "custom-config")
	t.Setenv("OPENCODE_CONFIG_DIR", custom)
	marker := filepath.Join(home, "plugin-runs")
	markerJSON, _ := json.Marshal(marker)
	root := custom
	if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export function record() { appendFileSync(%s, "run\n"); }`, markerJSON)
	if err := os.WriteFile(filepath.Join(root, "helper.js"), []byte(helper), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `import { record } from "../helper.js"; export default {id:"origin-test", async setup() {record();}};`
	if err := os.WriteFile(filepath.Join(root, "plugins", "sentinel.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	declaredMarker := filepath.Join(home, "declared-runs")
	declaredJSON, _ := json.Marshal(declaredMarker)
	declaredDir := filepath.Join(root, "declared")
	if err := os.MkdirAll(declaredDir, 0o755); err != nil {
		t.Fatal(err)
	}
	declared := fmt.Sprintf(`import { appendFileSync } from "node:fs"; export default {id:"declared-v2", async setup(host) {appendFileSync(%s, host.options.flag + "\n");}};`, declaredJSON)
	if err := os.WriteFile(filepath.Join(declaredDir, "index.js"), []byte(declared), 0o644); err != nil {
		t.Fatal(err)
	}
	const userJSON = `{"$schema":"https://opencode.ai/config.json","plugins":[{"package":"./declared","options":{"flag":"v2-option"}}]}`
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte(userJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "username.txt"), []byte("custom-user"), 0o600); err != nil {
		t.Fatal(err)
	}
	const userConfig = "{ // custom settings\n \"username\": \"{file:../username.txt}\",\n \"mcp\": {\"shared\":{\"type\":\"local\",\"command\":[\"custom-server\"],\"environment\":{\"TOKEN\":\"fixture-secret\"}}},\n}\n"
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := base.StageContext{ProjectRoot: project, Mode: mode.ImplicitEffective()}
	if err := prepare(stage); err != nil {
		t.Fatal(err)
	}
	stage.StagedRoots = map[base.Target]string{}
	pack := "pack-server"
	if err := writeMCP(mcp.Entries{"shared": {Server: mcp.Server{Command: &pack}}, "pack-only": {Server: mcp.Server{Command: &pack}}}, stage); err != nil {
		t.Fatal(err)
	}
	stageRoot, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ kind, name, content string }{
		{"agents", "pack-agent", "---\nname: pack-agent\ndescription: A pack agent\n---\n\nAgent fixture.\n"},
		{"commands", "pack-command", "---\ndescription: A pack command\n---\n\nCommand fixture.\n"},
	} {
		dir := filepath.Join(stageRoot, item.kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, item.name+".md"), []byte(item.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := injectGuidance("V2 pack guidance fixture.", stage); err != nil {
		t.Fatal(err)
	}
	bundle := hooks.Bundle{Hooks: []hooks.Hook{{Event: hooks.PreToolUse, Handler: hooks.Handler{Kind: hooks.CommandHandler, Command: "true"}, Origin: hooks.Origin{PackageKey: "fixture", SourceFile: "fixture"}}}}
	rendered, err := (HookRenderer{}).Render(bundle, hooks.RenderContext{TargetRoot: stageRoot, StagedPackages: map[string]string{"fixture": project}})
	if err != nil {
		t.Fatal(err)
	}
	bridgeMarker := filepath.Join(home, "bridge-runs")
	bridgeJSON, _ := json.Marshal(bridgeMarker)
	for index := range rendered.Files {
		if filepath.Base(rendered.Files[index].Path) == "index.js" {
			rendered.Files[index].Text += fmt.Sprintf("\nimport { appendFileSync } from 'node:fs'; appendFileSync(%s, 'run\\n');\n", bridgeJSON)
		}
	}
	if err := hooks.WriteRenderedFiles(rendered); err != nil {
		t.Fatal(err)
	}
	if err := finalizeSources(nil, stage); err != nil {
		t.Fatal(err)
	}
	staged, err := stagedRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	packConfig, err := os.ReadFile(filepath.Join(staged, "agentpack-config.json"))
	if err != nil || bytes.Contains(packConfig, []byte("fixture-secret")) || bytes.Contains(packConfig, []byte(`"shared"`)) || !bytes.Contains(packConfig, []byte("pack-only")) {
		t.Fatalf("pack config: %v %s", err, packConfig)
	}
	command, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "agents", "--standalone"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	commandWithTimeout := exec.CommandContext(ctx, command.Path, command.Args[1:]...)
	commandWithTimeout.Dir, commandWithTimeout.Env = project, command.Env
	if output, err := commandWithTimeout.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("pack-agent")) {
		t.Fatalf("native plugin: %v\n%s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "run\n" {
		t.Fatalf("plugin initialization: %q, %v", data, err)
	}
	bridgeData, err := os.ReadFile(bridgeMarker)
	if err != nil || string(bridgeData) != "run\n" {
		t.Fatalf("bridge initialization: %q, %v", bridgeData, err)
	}
	declaredData, err := os.ReadFile(declaredMarker)
	if err != nil || string(declaredData) != "v2-option\n" {
		t.Fatalf("declared plugin initialization: %q, %v", declaredData, err)
	}
	configCommand, err := launch(base.LaunchContext{ProjectRoot: project, Mode: stage.Mode, Arguments: []string{"debug", "config", "--standalone"}})
	if err != nil {
		t.Fatal(err)
	}
	configCommand.Dir = project
	configOutput, err := configCommand.CombinedOutput()
	if err != nil || !strings.Contains(string(configOutput), `"username": "custom-user"`) {
		t.Fatalf("native custom JSONC: %v\n%s", err, configOutput)
	}
	if !bytes.Contains(configOutput, []byte("pack-only")) || !bytes.Contains(configOutput, []byte("custom-server")) {
		t.Fatalf("native MCP config: %s", configOutput)
	}
	if !bytes.Contains(configOutput, []byte("pack-agent")) || !bytes.Contains(configOutput, []byte("Agent fixture.")) || !bytes.Contains(configOutput, []byte("pack-command")) || !bytes.Contains(configOutput, []byte("Command fixture.")) || !bytes.Contains(configOutput, []byte("AGENTS.md")) {
		t.Fatalf("pack agent and command discovery: %s", configOutput)
	}
	jsonData, err := os.ReadFile(filepath.Join(root, "opencode.json"))
	if err != nil || string(jsonData) != userJSON {
		t.Fatalf("custom JSON changed: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, "opencode.jsonc"))
	if err != nil || string(data) != userConfig {
		t.Fatalf("custom JSONC changed: %q, %v", data, err)
	}
}

func TestV2BridgeCallbackSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture hook executable uses a shell script")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is required to exercise the bridge")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "hook-runner")
	script := "#!/bin/sh\ncat >/dev/null\nif [ \"$6\" = pre ]; then head -c 131072 /dev/zero >&2; fi\nif [ \"$6\" = deny ]; then printf '%s\\n' '{\"decision\":\"deny\",\"message\":\"blocked\"}'; exit; fi\nprintf '%s\\n' '{\"decision\":\"allow\",\"updated_input\":{\"command\":\"changed\"},\"updated_tool_output\":\"after\",\"additional_context\":\"ctx\"}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"agentpack": fake, "hooks": []map[string]string{
		{"event": "tool.execute.before", "kind": "command", "specPath": "pre", "matcher": "bash"},
		{"event": "tool.execute.before", "kind": "command", "specPath": "deny", "matcher": "read"},
		{"event": "tool.execute.after", "kind": "command", "specPath": "post"},
		{"event": "permission.ask", "kind": "command", "specPath": "permission"},
		{"event": "chat.message", "kind": "command", "specPath": "prompt"},
		{"event": "experimental.session.compacting", "kind": "command", "specPath": "compact"},
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"index.js": []byte(pluginSource), "config.json": data, "check.js": []byte(`
import plugin from "./index.js";
import { strict as assert } from "node:assert";
const handlers = {};
const host = Object.fromEntries(["tool", "permission", "session"].map(domain => [domain, {hook: async (name, callback) => { handlers[domain + ":" + name] = callback; }}]));
await plugin.setup(host);
const before = {tool: "bash", input: {command:"original"}};
await handlers["tool:execute.before"](before);
assert.deepEqual(before.input, {command:"changed"});
await assert.rejects(() => handlers["tool:execute.before"]({tool:"read", input:{}}), /blocked/);
const after = {tool:"bash", status:"completed", result:{content:"old"}};
await handlers["tool:execute.after"](after);
assert.equal(after.result.content, "after\n\nctx");
const permission = {action:"bash", effect:"deny"};
await handlers["permission:evaluate"](permission);
assert.equal(permission.effect, "deny");
const prompt = {prompt:{text:"hello"}};
await handlers["session:prompt"](prompt);
assert.equal(prompt.prompt.text, "hello\n\nctx");
const compact = {system:[]};
await handlers["session:compaction"](compact);
assert.deepEqual(compact.system, [{type:"text", text:"ctx"}]);
`)} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bun, filepath.Join(root, "check.js"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bridge callbacks: %v\n%s", err, output)
	}
}
