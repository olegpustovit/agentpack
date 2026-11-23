package omp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

// TestGenerateSettingsUI regenerates settings-ui.json for a new OMP release:
//
//	OMP_SOURCE=<oh-my-pi checkout at the installed tag> go test ./internal/harness/omp -run TestGenerateSettingsUI
//
// Tabs, groups, labels and options come from the TypeScript registrations, which only the sources
// carry; types, defaults and complete enum values come from the installed omp, run against a
// throwaway agent directory. Registrations built in loops or helpers are skipped and logged.
func TestGenerateSettingsUI(t *testing.T) {
	source := os.Getenv("OMP_SOURCE")
	if source == "" {
		t.Skip("set OMP_SOURCE to an oh-my-pi checkout to regenerate settings-ui.json")
	}
	binary, err := base.ResolveBinary(base.OMP.Binary())
	if err != nil {
		t.Fatal(err)
	}
	agent := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command(binary, args...)
		cmd.Dir, cmd.Env = agent, append(os.Environ(), "PI_CODING_AGENT_DIR="+filepath.Join(agent, "agent"))
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	var listed map[string]struct {
		Value       json.RawMessage `json:"value"`
		Type        string          `json:"type"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal([]byte(run("config", "list", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		data, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	defs := read("packages/tui/src/overlays/settings-defs.ts")
	tabs := stringList(declaration(defs, "SETTING_TABS"))
	groups := map[string][]string{}
	for tab, list := range objectFields(declaration(defs, "TAB_GROUPS")) {
		groups[tab] = stringList(list)
	}
	const config = "packages/coding-agent/src/config"
	domains := read(config + "/all-settings.ts")
	imports := map[string]string{}
	for _, match := range regexp.MustCompile(`import \* as (\w+) from "([^"]+)"`).FindAllStringSubmatch(domains, -1) {
		imports[match[1]] = filepath.Join(config, match[2]+".ts")
	}
	order := elements(declaration(domains, "DOMAINS"))
	for _, match := range regexp.MustCompile(`domain: (\w+)`).FindAllStringSubmatch(declaration(domains, "PLACED_DOMAINS"), -1) {
		order = append(order, match[1])
	}
	var settings []Setting
	for _, domain := range order {
		file := read(imports[domain])
		for _, start := range regexp.MustCompile(`\bregister\(\s*\{`).FindAllStringIndex(file, -1) {
			open := start[1] - 1
			fields := objectFields(file[open : matching(file, open)+1])
			key, _ := literal(fields["id"])
			ui := objectFields(fields["ui"])
			if key == "" || ui == nil {
				if key == "" && fields["ui"] != "" {
					t.Logf("skipped dynamic registration in %s", imports[domain])
				}
				continue
			}
			setting, ok := panelSetting(t, key, ui, fields, file)
			if !ok {
				continue
			}
			entry := listed[key]
			setting.Type, setting.Default = entry.Type, entry.Value
			if setting.Description == "" {
				setting.Description = entry.Description
			}
			if setting.Type == "enum" {
				_, list, _ := strings.Cut(run("config", "set", "--", key, "agentpack-invalid-value"), "Valid values: ")
				setting.Values = strings.Split(strings.TrimSpace(list), ", ")
			}
			if visible(setting, ui) {
				settings = append(settings, setting)
			}
		}
	}
	// The model selector, not the panel, edits these; they get their own tab.
	for _, key := range []string{"modelRoles", "modelPresets", "enabledModels", "modelProviderOrder", "cycleOrder"} {
		entry := listed[key]
		settings = append(settings, Setting{Key: key, Tab: "roles", Label: key, Description: entry.Description, Type: entry.Type, Default: entry.Value})
	}
	tabs = append(tabs, "roles")
	slices.SortStableFunc(settings, func(a, b Setting) int {
		if a.Tab != b.Tab {
			return slices.Index(tabs, a.Tab) - slices.Index(tabs, b.Tab)
		}
		rank := func(s Setting) int {
			if s.Group == "" {
				return -1
			}
			if index := slices.Index(groups[s.Tab], s.Group); index >= 0 {
				return index
			}
			return len(groups[s.Tab])
		}
		return rank(a) - rank(b)
	})
	for i := range settings {
		if settings[i].Default == nil {
			settings[i].Default = json.RawMessage("null")
		}
	}
	data, err := json.MarshalIndent(settingsFile{OMP: strings.TrimPrefix(strings.TrimSpace(run("--version")), "omp/"), Settings: settings}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("settings-ui.json", append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d settings", len(settings))
}

// panelSetting reads the ui block; appearance only changes OMP's terminal display and credentials never leave a machine.
func panelSetting(t *testing.T, key string, ui, fields map[string]string, file string) (Setting, bool) {
	setting := Setting{Key: key}
	setting.Tab, _ = literal(ui["tab"])
	setting.Group, _ = literal(ui["group"])
	setting.Description, _ = literal(ui["description"])
	setting.Warning, _ = literal(ui["warning"])
	label, ok := literal(ui["label"])
	if !ok {
		t.Logf("skipped %s: label is not a literal", key)
		return setting, false
	}
	setting.Label = label
	if setting.Tab == "appearance" || fields["credential"] == "true" || ui["secret"] == "true" {
		return setting, false
	}
	options := ui["options"]
	if regexp.MustCompile(`^\w+$`).MatchString(options) {
		options = declaration(file, options)
	}
	for _, element := range elements(options) {
		option := objectFields(element)
		value, ok1 := literal(option["value"])
		label, ok2 := literal(option["label"])
		if !ok1 || !ok2 {
			continue
		}
		description, _ := literal(option["description"])
		setting.Options = append(setting.Options, Option{Value: value, Label: label, Description: description})
	}
	return setting, true
}

// visible applies the panel's rule for which settings get a row (settings-defs.ts entryToSettingDef).
func visible(setting Setting, ui map[string]string) bool {
	hasOptions := ui["options"] != "" && ui["options"] != `"runtime"`
	switch setting.Type {
	case "number":
		return hasOptions
	case "array":
		return ui["editor"] == `"json"` || hasOptions
	}
	return setting.Type != ""
}

// declaration returns the initializer of `const name = …` (or `export const name: T = …`).
func declaration(file, name string) string {
	match := regexp.MustCompile(`const ` + name + `\b[^=]*=\s*`).FindStringIndex(file)
	if match == nil || !strings.ContainsRune("[{", rune(file[match[1]])) {
		return ""
	}
	return file[match[1] : matching(file, match[1])+1]
}

// skip returns the index after the string, template or comment starting at i, or i+1.
func skip(s string, i int) int {
	switch {
	case s[i] == '"' || s[i] == '\'':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == s[i] {
				return j + 1
			}
		}
	case s[i] == '`':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == '$' && j+1 < len(s) && s[j+1] == '{' {
				j = matching(s, j+1)
			} else if s[j] == '`' {
				return j + 1
			}
		}
	case strings.HasPrefix(s[i:], "//"):
		if end := strings.IndexByte(s[i:], '\n'); end >= 0 {
			return i + end + 1
		}
	case strings.HasPrefix(s[i:], "/*"):
		if end := strings.Index(s[i:], "*/"); end >= 0 {
			return i + end + 2
		}
	default:
		return i + 1
	}
	return len(s)
}

// matching returns the index of the bracket closing the one at open.
func matching(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		switch s[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth == 0 {
				return i
			}
		}
		i = skip(s, i)
	}
	return len(s) - 1
}

// elements splits a bracketed literal into its top-level comma-separated parts.
func elements(text string) []string {
	if len(text) < 2 {
		return nil
	}
	var parts []string
	start, depth := 1, 0
	for i := 1; i < len(text)-1; {
		switch text[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(text[start:i]))
				start = i + 1
			}
		}
		i = skip(text, i)
	}
	parts = append(parts, strings.TrimSpace(text[start:len(text)-1]))
	return slices.DeleteFunc(parts, func(part string) bool { return stripComments(part) == "" })
}

// objectFields maps the keys of an object literal to their unevaluated initializers.
func objectFields(text string) map[string]string {
	if !strings.HasPrefix(text, "{") {
		return nil
	}
	fields := map[string]string{}
	for _, part := range elements(text) {
		part = stripComments(part)
		name, value, ok := strings.Cut(part, ":")
		if !ok || !regexp.MustCompile(`^["']?[\w$-]+["']?$`).MatchString(strings.TrimSpace(name)) {
			continue
		}
		fields[strings.Trim(strings.TrimSpace(name), `"'`)] = strings.TrimSpace(value)
	}
	return fields
}

func stripComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		next := skip(s, i)
		if !strings.HasPrefix(s[i:], "//") && !strings.HasPrefix(s[i:], "/*") {
			out.WriteString(s[i:next])
		}
		i = next
	}
	return strings.TrimSpace(out.String())
}

// literal evaluates a string literal or a `+` concatenation of them.
func literal(expression string) (string, bool) {
	expression = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expression), "as const"))
	if expression == "" {
		return "", false
	}
	var out strings.Builder
	for i := 0; i < len(expression); {
		switch c := expression[i]; {
		case c == '+' || c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"' || c == '\'' || c == '`':
			end := skip(expression, i)
			body := expression[i+1 : end-1]
			if c == '`' && strings.Contains(body, "${") {
				return "", false
			}
			if c != '`' {
				unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(strings.ReplaceAll(body, `\'`, `'`), `"`, `\"`) + `"`)
				if err != nil {
					return "", false
				}
				body = unquoted
			}
			out.WriteString(body)
			i = end
		default:
			return "", false
		}
	}
	return out.String(), true
}

func stringList(text string) []string {
	var list []string
	for _, element := range elements(text) {
		if value, ok := literal(element); ok {
			list = append(list, value)
		}
	}
	return list
}
