package omp

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// settings-ui.json is generated from the OMP sources by TestGenerateSettingsUI (see settings_gen_test.go).
//
//go:embed settings-ui.json
var settingsUI []byte

// Setting is one row of OMP's /settings panel, plus the model keys its model selector edits.
type Setting struct {
	Key         string          `json:"key"`
	Tab         string          `json:"tab"`
	Group       string          `json:"group,omitempty"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Warning     string          `json:"warning,omitempty"`
	Type        string          `json:"type"`
	Values      []string        `json:"values,omitempty"`
	Options     []Option        `json:"options,omitempty"`
	Default     json.RawMessage `json:"default"`
}

// Option is a labelled choice from the panel's submenu.
type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type settingsFile struct {
	OMP      string    `json:"omp"`
	Settings []Setting `json:"settings"`
}

var schema = sync.OnceValue(func() settingsFile {
	var file settingsFile
	if err := json.Unmarshal(settingsUI, &file); err != nil {
		panic(fmt.Sprintf("embedded OMP settings schema: %v", err))
	}
	return file
})

// Schema lists the settings agentpack can apply network-wide.
func Schema() []Setting { return schema().Settings }

// Lookup finds a schema entry by key.
func Lookup(key string) (Setting, bool) {
	index := slices.IndexFunc(Schema(), func(s Setting) bool { return s.Key == key })
	if index < 0 {
		return Setting{}, false
	}
	return Schema()[index], true
}

// RecordEntry finds the record setting that key names one entry of, like modelRoles.default, so
// a role is changed and reset on its own.
func RecordEntry(key string) (Setting, bool) {
	for _, setting := range Schema() {
		if setting.Type == "record" && len(key) > len(setting.Key)+1 && strings.HasPrefix(key, setting.Key+".") {
			return setting, true
		}
	}
	return Setting{}, false
}

// OverlayPath is the --config overlay the gateway writes from the replicated omp_settings table.
func OverlayPath() (string, error) {
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "omp", "settings.yml"), nil
}

// Settings reports this machine's OMP version and its effective values for the schema keys.
// An absent CLI returns an empty version and no error.
func Settings(ctx context.Context) (version string, values map[string]json.RawMessage, err error) {
	binary, err := base.LookPathEnv(base.OMP.Binary())
	if err != nil {
		return "", map[string]json.RawMessage{}, nil
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"--version"}, Timeout: 10 * time.Second})
	if err != nil {
		return "", nil, err
	}
	version = strings.TrimPrefix(strings.TrimSpace(result.Stdout), "omp/")
	result, err = base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"config", "list", "--json"}, Cwd: os.TempDir(), Timeout: 20 * time.Second, Categories: []string{"settings"}})
	if err != nil {
		return version, nil, err
	}
	var listed map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &listed); err != nil {
		return version, nil, fmt.Errorf("parse omp config list: %w", err)
	}
	values = map[string]json.RawMessage{}
	for _, setting := range Schema() {
		if entry, ok := listed[setting.Key]; ok && entry.Value != nil {
			values[setting.Key] = entry.Value
		}
	}
	return version, values, nil
}

// Models lists every model kind this machine's OMP can use, as `omp models --json` reports them.
func Models(ctx context.Context) ([]json.RawMessage, error) {
	binary, err := base.LookPathEnv(base.OMP.Binary())
	if err != nil {
		return []json.RawMessage{}, nil
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"models", "--json", "--kind", "all"}, Timeout: 30 * time.Second, Categories: []string{"models"}, Network: "provider_catalog"})
	if err != nil {
		return nil, err
	}
	var response struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return nil, fmt.Errorf("parse omp models: %w", err)
	}
	if response.Models == nil {
		response.Models = []json.RawMessage{}
	}
	return response.Models, nil
}

// Validate asks OMP to store the value in a throwaway agent directory and returns OMP's own
// complaint. Without OMP on this machine only the schema type is checked.
func Validate(ctx context.Context, key string, value json.RawMessage) error {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return fmt.Errorf("value is not JSON: %w", err)
	}
	setting, ok := Lookup(key)
	if !ok {
		// OMP accepts any record entry, so there is nothing more for it to check.
		if _, entry := RecordEntry(key); entry && decoded != nil {
			return nil
		}
		return fmt.Errorf("unknown OMP setting %q", key)
	}
	if !typeMatches(setting, decoded) {
		if setting.Type == "enum" {
			return fmt.Errorf("Valid values: %s", strings.Join(setting.Values, ", "))
		}
		return fmt.Errorf("%s expects a %s value", key, setting.Type)
	}
	binary, err := base.LookPathEnv(base.OMP.Binary())
	if err != nil {
		return nil
	}
	directory, err := os.MkdirTemp("", "agentpack-omp-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	text := string(value)
	if s, ok := decoded.(string); ok {
		text = s
	}
	result, err := base.RunProbe(ctx, base.ProbePlan{Executable: binary, Argv: []string{"config", "set", "--", key, text}, Cwd: directory, Env: append(os.Environ(), "PI_CODING_AGENT_DIR="+filepath.Join(directory, "agent")), Timeout: 20 * time.Second, Categories: []string{"settings"}})
	if err == nil {
		return nil
	}
	if result.ExitCode <= 0 {
		return err
	}
	message := strings.TrimSpace(result.Stderr)
	if message == "" {
		message = strings.TrimSpace(result.Stdout)
	}
	message, _, _ = strings.Cut(message, "\n")
	return errors.New(strings.TrimPrefix(message, "Error: "))
}

func typeMatches(setting Setting, value any) bool {
	switch setting.Type {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "enum":
		s, ok := value.(string)
		return ok && (len(setting.Values) == 0 || slices.Contains(setting.Values, s))
	case "array":
		_, ok := value.([]any)
		return ok
	case "record":
		_, ok := value.(map[string]any)
		return ok
	}
	return false
}

// RenderOverlay writes the values as an OMP config.yml overlay; dotted keys become nested maps,
// and an entry like modelRoles.default lands inside the modelRoles record, replacing its value.
func RenderOverlay(values map[string]json.RawMessage) ([]byte, error) {
	root := map[string]any{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		decoder := json.NewDecoder(bytes.NewReader(values[key]))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		parts := strings.Split(key, ".")
		node := root
		for _, part := range parts[:len(parts)-1] {
			child, ok := node[part].(map[string]any)
			if !ok {
				if _, taken := node[part]; taken {
					return nil, fmt.Errorf("%s conflicts with another setting", key)
				}
				child = map[string]any{}
				node[part] = child
			}
			node = child
		}
		// Keys sort parent first, so a record entry overrides the record's own value for it.
		node[parts[len(parts)-1]] = numbers(value)
	}
	return yaml.Marshal(root)
}

// numbers turns json.Number into int64 or float64 so YAML writes them unquoted and exact.
func numbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		f, _ := v.Float64()
		return f
	case map[string]any:
		for key, item := range v {
			v[key] = numbers(item)
		}
	case []any:
		for i, item := range v {
			v[i] = numbers(item)
		}
	}
	return value
}
