package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// nativeConfigFingerprint includes substitutions that can change server names
// without changing the containing config file.
func nativeConfigFingerprint(project, workspace string) (string, error) {
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
	var shadowed []string
	for name, entry := range entries {
		if user.mcp[name] {
			shadowed = append(shadowed, name)
		} else {
			filtered[name] = entry
		}
	}
	data, warnings, _, err := MergeMCPData(seed, filtered)
	if err != nil {
		return base.MCPProjection{}, err
	}
	return base.NewMCPProjection(entries, base.MCPProjectedFile{
		Path: packConfigPath(root), Contents: data, Scope: "mcp",
		MergeOrder: base.MCPMergeOrder("native user config (existing names win)"),
	}, warnings, shadowed, user.inputs)
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

// inheritedConfigPaths lists the user config files a native OpenCode launch may read.
func inheritedConfigPaths(workspace string) []string {
	roots, err := NativeUserConfigRoots()
	if err != nil {
		return nil
	}
	var result []string
	seen := make(map[string]bool)
	for _, root := range roots {
		for _, name := range []string{"opencode.json", "opencode.jsonc"} {
			path := filepath.Join(root, name)
			if !seen[path] {
				result = append(result, path)
				seen[path] = true
			}
		}
	}
	if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(workspace, explicit)
		}
		if !seen[explicit] {
			result = append(result, explicit)
		}
	}
	return result
}

// inheritedConfigSeed is the user config file seeded into the staged OpenCode root.
func inheritedConfigSeed() (string, string) {
	roots, err := NativeUserConfigRoots()
	if err != nil || len(roots) == 0 {
		return "", ""
	}
	path := filepath.Join(roots[0], "opencode.json")
	return path, path
}

// launchInputs is everything OpenCode staging reads besides the pack: the
// config roots and their files, custom asset names, project config up to the
// repository root, the config environment and the binary, whose major version
// selects the layout.
func launchInputs(projectRoot, workspace string) (string, error) {
	hash := sha256.New()
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	globalConfig := os.Getenv("XDG_CONFIG_HOME")
	if globalConfig == "" {
		globalConfig = filepath.Join(home, ".config")
	}
	globalConfig = filepath.Join(globalConfig, "opencode")
	configDir := os.Getenv("OPENCODE_CONFIG_DIR")
	if configDir == "" {
		configDir = globalConfig
	}
	configDir, err = filepath.Abs(configDir)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(hash, "opencode_config_dir\x00%s", configDir)
	entries, readErr := os.ReadDir(configDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", readErr
	}
	for _, entry := range entries {
		fmt.Fprintf(hash, "\x00custom_entry\x00%s\x00%s", entry.Name(), entry.Type())
	}
	for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
		base.DigestFile(hash, "opencode_user_"+name, filepath.Join(configDir, name))
		base.DigestFile(hash, "opencode_global_"+name, filepath.Join(globalConfig, name))
		base.DigestFile(hash, "opencode_seed_"+name, filepath.Join(home, ".config", "opencode", name))
	}
	base.DigestFile(hash, "opencode_cli_config", filepath.Join(configDir, "cli.json"))
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		base.DigestFile(hash, "opencode_home_"+name, filepath.Join(home, ".opencode", name))
	}
	if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(workspace, explicit)
		}
		base.DigestFile(hash, "opencode_explicit_config\x00"+explicit, explicit)
	}
	fmt.Fprintf(hash, "opencode_inline_config\x00%s\x00opencode_disable_project_config\x00%s", os.Getenv("OPENCODE_CONFIG_CONTENT"), os.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG"))
	for directory := workspace; ; directory = filepath.Dir(directory) {
		for _, name := range []string{"opencode.json", "opencode.jsonc", ".opencode/opencode.json", ".opencode/opencode.jsonc"} {
			base.DigestFile(hash, "opencode_project\x00"+directory+"\x00"+name, filepath.Join(directory, filepath.FromSlash(name)))
		}
		if _, statErr := os.Stat(filepath.Join(directory, ".git")); statErr == nil || filepath.Dir(directory) == directory {
			break
		}
	}
	for _, name := range []string{"plugin", "plugins"} {
		info, statErr := os.Stat(filepath.Join(configDir, name))
		fmt.Fprintf(hash, "\x00%s\x00%t", name, statErr == nil && info.IsDir())
	}
	for _, name := range []string{"skill", "skills", "agent", "agents", "command", "commands", "mode", "modes"} {
		if err := writeAssetNames(hash, filepath.Join(configDir, name)); err != nil {
			return "", err
		}
	}
	native, err := nativeConfigFingerprint(projectRoot, workspace)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(hash, "opencode_native_inputs\x00%s", native)
	if binary, err := base.OpenCode.Resolve(); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(binary); resolveErr == nil {
			binary = resolved
		}
		if info, statErr := os.Stat(binary); statErr == nil {
			fmt.Fprintf(hash, "opencode_binary\x00%s\x00%d\x00%d", binary, info.Size(), info.ModTime().UnixNano())
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// writeAssetNames records the names and metadata of custom assets, not their bytes.
func writeAssetNames(hash io.Writer, root string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if os.IsNotExist(err) {
		fmt.Fprintf(hash, "custom_assets\x00%s\x00absent", root)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(hash, "custom_assets\x00%s\x00%s", root, resolved)
	return filepath.WalkDir(resolved, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "\x00%s\x00%s\x00%d\x00%d", relative, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
}
