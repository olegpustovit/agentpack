package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/tailscale/hujson"
)

const instructionsFile = "agentpack-no-attribution.md"

func New() base.Harness {
	return base.Definition{Target: base.OpenCode, Root: stagedRoot, Setup: prepare, ProjectMCP: planMCP, ProjectLSP: planLSP, LSP: writeLSP, Guidance: injectGuidance, AfterStage: finalizeSources, Check: verify, Launch: launch}
}

func launch(ctx base.LaunchContext) (*exec.Cmd, error) {
	if nativeMajor() < 0 {
		return nil, fmt.Errorf("unsupported OpenCode major version; use a tested OpenCode 1 or 2 release")
	}
	root, err := paths.StagingOpenCodeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return nil, err
	}
	binary, err := base.ResolveBinary("OPENCODE_PATH", "opencode")
	if err != nil {
		return nil, err
	}
	command := exec.Command(binary, ctx.Arguments...)
	major := nativeMajor()
	command.Env = os.Environ()
	if major != 1 && major != 2 {
		command.Env = append(command.Env, "OPENCODE_CONFIG_DIR="+root)
	}
	if ctx.Yolo || major == 1 || packConfigPath(root) != filepath.Join(root, "opencode.json") {
		value := make(map[string]any)
		if major == 1 || packConfigPath(root) != filepath.Join(root, "opencode.json") {
			data, err := os.ReadFile(packConfigPath(root))
			if err != nil {
				return nil, err
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			if value == nil {
				return nil, fmt.Errorf("generated OpenCode config must be an object")
			}
			for _, key := range []string{"plugin", "instructions"} {
				if list, ok := value[key].([]any); ok {
					for i, item := range list {
						if path, ok := item.(string); ok && !filepath.IsAbs(path) && !strings.Contains(path, ":") {
							abs := filepath.Join(root, path)
							if key == "plugin" {
								urlPath := filepath.ToSlash(abs)
								if !strings.HasPrefix(urlPath, "/") {
									urlPath = "/" + urlPath
								}
								list[i] = (&url.URL{Scheme: "file", Path: urlPath}).String()
							} else {
								list[i] = abs
							}
						}
					}
				}
			}
		}
		var placeholders map[string]string
		if content := os.Getenv("OPENCODE_CONFIG_CONTENT"); content != "" {
			reserved, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			content, placeholders = protectInlineVariables(content, string(reserved))
			standard, err := hujson.Standardize([]byte(content))
			if err != nil {
				return nil, fmt.Errorf("parse OPENCODE_CONFIG_CONTENT: %w", err)
			}
			var user map[string]any
			decoder := json.NewDecoder(bytes.NewReader(standard))
			decoder.UseNumber()
			if err := decoder.Decode(&user); err != nil {
				return nil, fmt.Errorf("parse OPENCODE_CONFIG_CONTENT: %w", err)
			}
			if user == nil {
				return nil, fmt.Errorf("OPENCODE_CONFIG_CONTENT must be an object")
			}
			if major == 2 {
				mergeSkills(value, user)
			}
			mergeInline(value, user)
		}
		if ctx.Yolo {
			value["permission"] = "allow"
			if _, present := value["permissions"]; present {
				value["permissions"] = []map[string]string{{"action": "*", "resource": "*", "effect": "allow"}}
			}
		}
		content, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		for placeholder, original := range placeholders {
			encoded, _ := json.Marshal(placeholder)
			content = bytes.ReplaceAll(content, encoded, []byte(original))
		}
		command.Env = append(command.Env, "OPENCODE_CONFIG_CONTENT="+string(content))
	}
	// OpenCode gives its model the lsp tool only with this variable set;
	// a package's language servers are staged so that the agent uses them.
	if _, set := os.LookupEnv("OPENCODE_EXPERIMENTAL_LSP_TOOL"); !set && stagesLSP(packConfigPath(root)) {
		command.Env = append(command.Env, "OPENCODE_EXPERIMENTAL_LSP_TOOL=true")
	}
	return command, nil
}

func stagedRoot(ctx base.StageContext) (string, error) {
	return paths.StagingOpenCodeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
}

func prepare(ctx base.StageContext) error {
	if nativeMajor() < 0 {
		return fmt.Errorf("unsupported OpenCode major version; use a tested OpenCode 1 or 2 release")
	}
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err == nil {
		major := nativeMajor()
		var entries []string
		if major == 0 {
			entries = append(entries, "opencode.json", "agents", "commands", "modes", "skills")
		}
		if major == 0 {
			entries = append(entries, "plugins")
		}
		source, err := userConfigRoot(home, major)
		if err != nil {
			return err
		}
		if err := separateUserConfig(source, root); err != nil {
			return err
		}
		if major == 1 && os.Getenv("OPENCODE_CONFIG_DIR") != "" {
			if err := separateUserConfig(os.Getenv("OPENCODE_CONFIG_DIR"), root); err != nil {
				return err
			}
		}
		if err := base.CopySelectedEntries(source, root, entries); err != nil {
			return err
		}
	}
	configPath := packConfigPath(root)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := writeConfig(configPath, map[string]any{"$schema": "https://opencode.ai/config.json"}); err != nil {
			return err
		}
	}
	if !keepAttribution() {
		return forceAttributionOff(root)
	}
	return nil
}

func separateUserConfig(source, staged string) error {
	source, err := filepath.EvalSymlinks(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect OpenCode config directory: %w", err)
	}
	staged, err = filepath.EvalSymlinks(staged)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{source, staged}, {staged, source}} {
		if relative, err := filepath.Rel(pair[0], pair[1]); err == nil && filepath.IsLocal(relative) {
			return fmt.Errorf("OpenCode config directory overlaps agentpack staging: %s", source)
		}
	}
	return nil
}

func userConfigRoot(home string, major int) (string, error) {
	if major == 2 && os.Getenv("OPENCODE_CONFIG_DIR") != "" {
		return filepath.Abs(os.Getenv("OPENCODE_CONFIG_DIR"))
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return filepath.Abs(filepath.Join(config, "opencode"))
}

var versionCache sync.Map

func nativeMajor() int {
	binary, err := base.ResolveBinary("OPENCODE_PATH", "opencode")
	if err != nil {
		return 0
	}
	info, err := os.Stat(binary)
	if err != nil {
		return 0
	}
	key := fmt.Sprintf("%s\x00%d\x00%d", binary, info.Size(), info.ModTime().UnixNano())
	if cached, ok := versionCache.Load(key); ok {
		return cached.(int)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return 0
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(output)), "opencode v")
	major := 0
	if strings.HasPrefix(version, "1.") {
		major = 1
	} else if strings.HasPrefix(version, "2.") {
		major = 2
	} else {
		var vmajor, vminor, vpatch int
		if count, _ := fmt.Sscanf(version, "%d.%d.%d", &vmajor, &vminor, &vpatch); count == 3 {
			major = -1
		}
	}
	versionCache.Store(key, major)
	return major
}

func forceAttributionOff(root string) error {
	if err := os.WriteFile(filepath.Join(root, instructionsFile), []byte(base.NoAttributionBody), 0o644); err != nil {
		return err
	}
	path := packConfigPath(root)
	seed, err := base.ReadMCPSeed(path)
	if err != nil {
		return err
	}
	data, err := AttributionData(seed)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func AttributionData(seed []byte) ([]byte, error) {
	value := make(map[string]any)
	if len(seed) != 0 {
		standard, err := hujson.Standardize(append(seed, '\n'))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(standard, &value); err != nil {
			return nil, err
		}
	}
	instructions, _ := value["instructions"].([]any)
	found := false
	for _, instruction := range instructions {
		if instruction == instructionsFile {
			found = true
		}
	}
	if !found {
		instructions = append(instructions, instructionsFile)
	}
	value["instructions"] = instructions
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func writeMCP(entries mcp.Entries, ctx base.StageContext) error {
	projection, err := planMCP(entries, ctx)
	if err != nil {
		return err
	}
	return base.WriteMCPProjection(projection)
}
func planMCP(entries mcp.Entries, ctx base.StageContext) (base.MCPProjection, error) {
	root, err := stagedRoot(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	if nativeMajor() == 1 || nativeMajor() == 2 {
		return planNativeMCP(entries, ctx, root)
	}
	seedPath := filepath.Join(root, "opencode.json")
	if ctx.StagedRoots[base.OpenCode] == "" {
		if home, err := os.UserHomeDir(); err == nil {
			userRoot, err := userConfigRoot(home, nativeMajor())
			if err != nil {
				return base.MCPProjection{}, err
			}
			seedPath = filepath.Join(userRoot, "opencode.json")
		}
	}
	seed, err := base.ReadMCPSeed(seedPath)
	if err != nil {
		return base.MCPProjection{}, err
	}
	if ctx.StagedRoots == nil && !keepAttribution() {
		seed, err = AttributionData(seed)
		if err != nil {
			return base.MCPProjection{}, err
		}
	}
	inputPath := seedPath
	if ctx.StagedRoots[base.OpenCode] != "" {
		if home, err := os.UserHomeDir(); err == nil {
			userRoot, err := userConfigRoot(home, nativeMajor())
			if err != nil {
				return base.MCPProjection{}, err
			}
			inputPath = filepath.Join(userRoot, "opencode.json")
		}
	}
	input, err := base.ReadMCPInput(inputPath)
	if err != nil {
		return base.MCPProjection{}, err
	}
	inputs := []base.MCPProjectedInput{input}
	if nativeMajor() == 2 {
		jsonc, err := base.ReadMCPInput(filepath.Join(filepath.Dir(inputPath), "opencode.jsonc"))
		if err != nil {
			return base.MCPProjection{}, err
		}
		inputs = append(inputs, jsonc)
	}
	data, warnings, shadowed, err := MergeMCPData(seed, entries)
	if err != nil {
		return base.MCPProjection{}, err
	}
	servers := base.ProjectedMCPServers(entries)
	for index := range servers {
		if slices.Contains(shadowed, servers[index].Name) {
			servers[index].Winner = "user"
		}
	}
	base.MarkMCPWarnings(servers, warnings)
	digest, err := base.MCPJSONScopeDigest(data, "mcp")
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.MCPProjection{Files: []base.MCPProjectedFile{{Path: filepath.Join(root, "opencode.json"), Contents: data, Scope: "mcp", ScopeDigest: digest, MergeOrder: []string{"user config (existing names win)", "plugin definitions", "agentpack.toml", ".agents/mcp.json"}}}, Servers: servers, Warnings: warnings, Inputs: inputs}, nil
}
func injectGuidance(blob string, ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	return base.WriteGuidance(filepath.Join(root, "AGENTS.md"), blob)
}
func verify(ctx base.StageContext) error {
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("opencode staging missing %s", root)
	}
	return nil
}
func keepAttribution() bool {
	switch os.Getenv("AGENTPACK_KEEP_ATTRIBUTION") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func writeConfig(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// stagesLSP says whether the staged opencode.json names a language server.
func stagesLSP(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	standard, err := hujson.Standardize(append(data, '\n'))
	if err != nil {
		return false
	}
	var config struct {
		LSP map[string]json.RawMessage `json:"lsp"`
	}
	return json.Unmarshal(standard, &config) == nil && len(config.LSP) != 0
}

func packConfigPath(root string) string {
	if nativeMajor() == 2 {
		return filepath.Join(root, "agentpack-config.json")
	}
	if nativeMajor() == 1 && os.Getenv("OPENCODE_CONFIG_DIR") != "" {
		home, err := os.UserHomeDir()
		if err == nil {
			global, err := userConfigRoot(home, 1)
			custom, absErr := filepath.Abs(os.Getenv("OPENCODE_CONFIG_DIR"))
			if err == nil && absErr == nil && filepath.Clean(global) != filepath.Clean(custom) && !base.DurablePathMatches(global, custom) {
				return filepath.Join(root, "agentpack-config.json")
			}
		}
	}
	return filepath.Join(root, "opencode.json")
}

func mergeInline(dst, src map[string]any) {
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			if target, ok := dst[key].(map[string]any); ok {
				mergeInline(target, nested)
				continue
			}
		}
		if key == "instructions" || key == "plugin" {
			if incoming, ok := value.([]any); ok {
				if existing, ok := dst[key].([]any); ok {
					dst[key] = append(existing, incoming...)
					continue
				}
			}
		}
		dst[key] = value
	}
}

func mergeSkills(dst, src map[string]any) {
	pack, ok := dst["skills"].(map[string]any)
	if !ok {
		return
	}
	paths, ok := pack["paths"].([]any)
	if !ok {
		return
	}
	switch skills := src["skills"].(type) {
	case []any:
		src["skills"] = append(paths, skills...)
	case map[string]any:
		if incoming, ok := skills["paths"].([]any); ok {
			skills["paths"] = append(paths, incoming...)
		}
	}
}

// protectInlineVariables leaves unquoted native substitutions for OpenCode to expand.
func protectInlineVariables(input, reserved string) (string, map[string]string) {
	replacements := make(map[string]string)
	var out strings.Builder
	quoted, escaped, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(input); {
		c := input[i]
		if lineComment {
			out.WriteByte(c)
			i++
			if c == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			if c == '*' && i+1 < len(input) && input[i+1] == '/' {
				out.WriteString("*/")
				i += 2
				blockComment = false
			} else {
				out.WriteByte(c)
				i++
			}
			continue
		}
		if quoted {
			out.WriteByte(c)
			i++
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			out.WriteByte(c)
			i++
			continue
		}
		if c == '/' && i+1 < len(input) && (input[i+1] == '/' || input[i+1] == '*') {
			lineComment = input[i+1] == '/'
			blockComment = !lineComment
			out.WriteString(input[i : i+2])
			i += 2
			continue
		}
		if c == '{' && (strings.HasPrefix(input[i:], "{env:") || strings.HasPrefix(input[i:], "{file:")) {
			if end := strings.IndexByte(input[i:], '}'); end >= 0 {
				token := input[i : i+end+1]
				marker := fmt.Sprintf("__agentpack_inline_variable_%d__", len(replacements))
				for strings.Contains(input, marker) || strings.Contains(reserved, marker) {
					marker += "_"
				}
				replacements[marker] = token
				encoded, _ := json.Marshal(marker)
				out.Write(encoded)
				i += end + 1
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String(), replacements
}
