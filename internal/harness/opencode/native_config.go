package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/mcp"
	"github.com/tailscale/hujson"
)

type nativeServers struct {
	mcp, lsp    map[string]bool
	lspDisabled bool
	inputs      []base.MCPProjectedInput
	env         map[string]string
}

var configEnvVariable = regexp.MustCompile(`\{env:([^}]+)\}`)
var configFileVariable = regexp.MustCompile(`\{file:([^}]+)\}`)

// NativeUserConfigRoots lists the user directories the selected runtime reads.
func NativeUserConfigRoots() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	major := nativeMajor()
	root, err := userConfigRoot(home, major)
	if err != nil {
		return nil, err
	}
	roots := []string{root}
	if major != 2 {
		roots = append(roots, filepath.Join(home, ".opencode"))
		if custom := os.Getenv("OPENCODE_CONFIG_DIR"); custom != "" {
			absolute, err := filepath.Abs(custom)
			if err != nil {
				return nil, err
			}
			if filepath.Clean(root) != filepath.Clean(absolute) && !base.DurablePathMatches(root, absolute) {
				roots = append(roots, absolute)
			}
		}
	}
	return roots, nil
}

// NativeConfigFingerprint includes substitutions that can change server names
// without changing the containing config file.
func NativeConfigFingerprint(project, workspace string) (string, error) {
	if nativeMajor() != 1 && nativeMajor() != 2 {
		return "", nil
	}
	config, err := nativeServerNames(base.StageContext{ProjectRoot: project, WorkspaceRoot: workspace})
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, input := range config.inputs {
		fmt.Fprintf(hash, "%s\x00%t\x00%s\x00", input.Path, input.Exists, input.Digest)
	}
	names := make([]string, 0, len(config.env))
	for name := range config.env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(hash, "%s\x00%s\x00", name, config.env[name])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func nativeSeed(ctx base.StageContext, root string) ([]byte, error) {
	seed := append([]byte(nil), ctx.SeedConfig...)
	if len(seed) == 0 && ctx.StagedRoots != nil {
		var err error
		seed, err = base.ReadMCPSeed(packConfigPath(root))
		if err != nil {
			return nil, err
		}
	}
	if len(seed) == 0 {
		seed = []byte(`{}`)
	}
	if ctx.StagedRoots == nil && !keepAttribution() {
		return AttributionData(seed)
	}
	return seed, nil
}

func planNativeMCP(entries mcp.Entries, ctx base.StageContext, root string) (base.MCPProjection, error) {
	user, err := nativeServerNames(ctx)
	if err != nil {
		return base.MCPProjection{}, err
	}
	seed, err := nativeSeed(ctx, root)
	if err != nil {
		return base.MCPProjection{}, err
	}
	filtered := make(mcp.Entries)
	servers := base.ProjectedMCPServers(entries)
	for index := range servers {
		name := servers[index].Name
		if user.mcp[name] {
			servers[index].Winner = "user"
		} else {
			filtered[name] = entries[name]
		}
	}
	data, warnings, _, err := MergeMCPData(seed, filtered)
	if err != nil {
		return base.MCPProjection{}, err
	}
	base.MarkMCPWarnings(servers, warnings)
	digest, err := base.MCPJSONScopeDigest(data, "mcp")
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.MCPProjection{Files: []base.MCPProjectedFile{{Path: packConfigPath(root), Contents: data, Scope: "mcp", ScopeDigest: digest, MergeOrder: []string{"native user config (existing names win)", "plugin definitions", "agentpack.toml", ".agents/mcp.json"}}}, Servers: servers, Warnings: warnings, Inputs: user.inputs}, nil
}

func planNativeLSP(servers map[string]json.RawMessage, ctx base.StageContext, root string) (base.LSPProjection, error) {
	user, err := nativeServerNames(ctx)
	if err != nil {
		return base.LSPProjection{}, err
	}
	filtered := make(map[string]json.RawMessage)
	var omitted []string
	for name, server := range servers {
		if user.lspDisabled || user.lsp[name] {
			omitted = append(omitted, name)
		} else {
			filtered[name] = server
		}
	}
	sort.Strings(omitted)
	var warnings []string
	if user.lspDisabled {
		warnings = append(warnings, fmt.Sprintf("OpenCode config disables LSP; skipped %d language servers", len(omitted)))
	}
	if len(filtered) == 0 {
		return base.LSPProjection{Warnings: warnings, Omitted: omitted}, nil
	}
	seed, err := nativeSeed(ctx, root)
	if err != nil {
		return base.LSPProjection{}, err
	}
	data, extraWarnings, extraOmitted, err := MergeLSPData(seed, filtered)
	if err != nil {
		return base.LSPProjection{}, err
	}
	return base.LSPProjection{Files: []base.MCPProjectedFile{{Path: packConfigPath(root), Contents: data, Scope: "lsp"}}, Warnings: append(warnings, extraWarnings...), Omitted: append(omitted, extraOmitted...)}, nil
}

// nativeServerNames reads names only; user server definitions stay in native files.
func nativeServerNames(ctx base.StageContext) (nativeServers, error) {
	result := nativeServers{mcp: map[string]bool{}, lsp: map[string]bool{}, env: map[string]string{}}
	home, err := os.UserHomeDir()
	if err != nil {
		return result, err
	}
	major := nativeMajor()
	root, err := userConfigRoot(home, major)
	if err != nil {
		return result, err
	}
	paths := []string{filepath.Join(root, "opencode.json"), filepath.Join(root, "opencode.jsonc")}
	if major != 2 {
		paths = append([]string{filepath.Join(root, "config.json")}, paths...)
	}
	workspace := ctx.WorkspaceRoot
	if workspace == "" {
		workspace = ctx.ProjectRoot
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return result, err
	}
	if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(workspace, explicit)
		}
		paths = append(paths, explicit)
	}
	if flag := strings.ToLower(os.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG")); flag != "1" && flag != "true" {
		var parents []string
		for dir := workspace; ; dir = filepath.Dir(dir) {
			parents = append(parents, dir)
			if _, err := os.Stat(filepath.Join(dir, ".git")); (major != 2 && err == nil) || filepath.Dir(dir) == dir {
				break
			}
		}
		for i := len(parents) - 1; i >= 0; i-- {
			for _, name := range []string{"opencode.json", "opencode.jsonc"} {
				paths = append(paths, filepath.Join(parents[i], name))
			}
		}
		for _, dir := range parents {
			for _, name := range []string{"opencode.json", "opencode.jsonc"} {
				paths = append(paths, filepath.Join(dir, ".opencode", name))
			}
		}
	}
	if major != 2 {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			paths = append(paths, filepath.Join(home, ".opencode", name))
		}
	}
	if custom := os.Getenv("OPENCODE_CONFIG_DIR"); major != 2 && custom != "" {
		custom, err = filepath.Abs(custom)
		if err != nil {
			return result, err
		}
		if !base.DurablePathMatches(root, custom) {
			for _, name := range []string{"opencode.json", "opencode.jsonc"} {
				paths = append(paths, filepath.Join(custom, name))
			}
		}
	}
	parse := func(data []byte, source, baseDir string) error {
		if len(data) == 0 {
			return nil
		}
		text := configEnvVariable.ReplaceAllStringFunc(string(data), func(token string) string {
			name := configEnvVariable.FindStringSubmatch(token)[1]
			result.env[name] = os.Getenv(name)
			return result.env[name]
		})
		var expanded strings.Builder
		cursor := 0
		for _, match := range configFileVariable.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[0], match[1]
			expanded.WriteString(text[cursor:start])
			line := strings.LastIndex(text[:start], "\n") + 1
			if strings.HasPrefix(strings.TrimLeft(text[line:start], " \t\r"), "//") {
				expanded.WriteString(text[start:end])
				cursor = end
				continue
			}
			file := text[match[2]:match[3]]
			if strings.HasPrefix(file, "~/") {
				file = filepath.Join(home, file[2:])
			} else if !filepath.IsAbs(file) {
				file = filepath.Join(baseDir, file)
			}
			contents, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read OpenCode file reference %s: %w", file, err)
			}
			input, err := base.MCPInput(file, contents)
			if err != nil {
				return err
			}
			result.inputs = append(result.inputs, input)
			quoted, err := json.Marshal(strings.TrimSpace(string(contents)))
			if err != nil {
				return err
			}
			expanded.Write(quoted[1 : len(quoted)-1])
			cursor = end
		}
		expanded.WriteString(text[cursor:])
		standard, err := hujson.Standardize(append([]byte(expanded.String()), '\n'))
		if err != nil {
			return fmt.Errorf("inspect OpenCode config %s: invalid JSON or JSONC", source)
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(standard, &value); err != nil || value == nil {
			return fmt.Errorf("inspect OpenCode config %s: expected a JSON object", source)
		}
		for _, section := range []struct {
			key   string
			names map[string]bool
		}{{"mcp", result.mcp}, {"lsp", result.lsp}} {
			raw, ok := value[section.key]
			if !ok || string(raw) == "null" {
				continue
			}
			if section.key == "lsp" && string(raw) == "false" {
				result.lspDisabled = true
				continue
			}
			var names map[string]json.RawMessage
			if err := json.Unmarshal(raw, &names); err != nil {
				return fmt.Errorf("inspect OpenCode config %s: %s must be an object", source, section.key)
			}
			if section.key == "lsp" {
				result.lspDisabled = false
			}
			for name, value := range names {
				if major == 2 && section.key == "mcp" && name == "servers" {
					var native map[string]json.RawMessage
					var legacyType string
					if json.Unmarshal(value, &native) == nil && json.Unmarshal(native["type"], &legacyType) != nil {
						for server := range native {
							section.names[server] = true
						}
						continue
					}
				}
				section.names[name] = true
			}
		}
		return nil
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		data, err := base.ReadMCPSeed(path)
		if err != nil {
			return result, err
		}
		input, err := base.MCPInput(path, data)
		if err != nil {
			return result, err
		}
		result.inputs = append(result.inputs, input)
		if err := parse(data, path, filepath.Dir(path)); err != nil {
			return result, err
		}
	}
	if err := parse([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), "OPENCODE_CONFIG_CONTENT", workspace); err != nil {
		return result, err
	}
	return result, nil
}
