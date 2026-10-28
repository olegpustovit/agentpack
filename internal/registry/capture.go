package registry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/olegpustovit/agentpack/internal/lockfile"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

var (
	credentialKey   = regexp.MustCompile(`(?i)(token|secret|password|passwd|(?:api|private|client)[_-]?key|authorization|credential|auth|signature|sig)`)
	credentialValue = regexp.MustCompile(`(?i)(<local>|bearer\s+[^\s,;]+|basic\s+[^\s,;]+|(?:token|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|authorization)\s*[=:]\s*[^\s,;]+|://[^/@\s]+:[^/@\s]+@|\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})\b)`)
	credentialFlag  = regexp.MustCompile(`(?i)^--?(?:api[_-]?key|token|access[_-]?token|refresh[_-]?token|password|passwd|secret|client[_-]?secret|authorization|credential)$`)
)

func normalizeTOML(input []byte) ([]byte, map[string]string, error) {
	var value map[string]any
	if err := toml.Unmarshal(input, &value); err != nil {
		return nil, nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	secrets := map[string]string{}
	redact(value, "", "", secrets)
	data, err := toml.Marshal(value)
	return data, secrets, err
}

func redact(value any, path, parent string, secrets map[string]string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			childPath := path + "/" + escapePointer(key)
			if key == "env" || key == "headers" {
				if values, ok := child.(map[string]any); ok {
					for name, item := range values {
						if raw, ok := item.(string); ok {
							saveSecret(secrets, childPath+"/"+escapePointer(name), raw)
							values[name] = ""
						} else {
							redact(item, childPath+"/"+escapePointer(name), name, secrets)
						}
					}
					continue
				}
			}
			if credentialKey.MatchString(key) {
				if raw, ok := child.(string); ok && raw != "" {
					saveSecret(secrets, childPath, raw)
					current[key] = "<local>"
					continue
				}
			}
			if raw, ok := child.(string); ok && credentialShaped(raw) {
				saveSecret(secrets, childPath, raw)
				current[key] = "<local>"
				continue
			}
			redact(child, childPath, key, secrets)
		}
	case []any:
		previousWasSecretFlag := false
		for i, child := range current {
			childPath := path + "/" + fmt.Sprint(i)
			if raw, ok := child.(string); ok {
				if previousWasSecretFlag {
					previousWasSecretFlag = false
					if !strings.HasPrefix(raw, "-") {
						saveSecret(secrets, childPath, raw)
						current[i] = "<local>"
						continue
					}
				}
				if credentialShaped(raw) {
					saveSecret(secrets, childPath, raw)
					current[i] = "<local>"
					continue
				}
				previousWasSecretFlag = credentialFlag.MatchString(raw)
				continue
			}
			redact(child, childPath, parent, secrets)
			previousWasSecretFlag = false
		}
	case string:
		if credentialShaped(current) {
			saveSecret(secrets, path, current)
		}
	}
}

func saveSecret(secrets map[string]string, path, value string) {
	if path != "" {
		secrets[path] = value
	}
}

func redactMetadata(value string) string {
	if credentialShaped(value) {
		return "<local>"
	}
	return value
}

func credentialShaped(value string) bool {
	if credentialValue.MatchString(value) {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.User != nil {
		return true
	}
	for key := range parsed.Query() {
		key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
		if key == "key" || credentialKey.MatchString(key) {
			return true
		}
	}
	return false
}

func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func mergeBindings(a, b map[string]string, ap, bp string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[ap+k] = v
	}
	for k, v := range b {
		out[bp+k] = v
	}
	return out
}

func applyBindings(document []byte, values map[string]string, prefix string) ([]byte, error) {
	var value any
	if err := toml.Unmarshal(document, &value); err != nil {
		return nil, err
	}
	matched := 0
	for path, secret := range values {
		if strings.HasPrefix(path, prefix) {
			if err := setPath(value, strings.TrimPrefix(path, prefix), secret); err != nil {
				return nil, err
			}
			matched++
		}
	}
	if matched == 0 && strings.Contains(string(document), "<local>") {
		return nil, fmt.Errorf("missing local binding")
	}
	restored, err := toml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return restored, nil
}

func setPath(value any, path, secret string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("invalid local binding path")
	}
	current := value
	for i, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		last := i == len(parts)-1
		switch node := current.(type) {
		case map[string]any:
			if last {
				if _, ok := node[part]; !ok {
					return fmt.Errorf("local binding path does not exist")
				}
				node[part] = secret
				return nil
			}
			next, ok := node[part]
			if !ok {
				return fmt.Errorf("local binding path does not exist")
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return fmt.Errorf("invalid local binding path")
			}
			if last {
				node[index] = secret
				return nil
			}
			current = node[index]
		default:
			return fmt.Errorf("local binding path does not exist")
		}
	}
	return fmt.Errorf("invalid local binding path")
}

func (s *Store) revisionPackages(revision Revision) (map[string]Package, error) {
	out := make(map[string]Package, len(revision.PackageIDs))
	for _, id := range revision.PackageIDs {
		p, err := s.readPackage(id)
		if err != nil {
			return nil, err
		}
		out[p.Module] = p
	}
	return out, nil
}

func decodeLock(data []byte) (lockfile.PackLock, error) {
	directory, err := os.MkdirTemp("", "agentpack-registry-lock-")
	if err != nil {
		return lockfile.PackLock{}, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "pack.lock")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return lockfile.PackLock{}, err
	}
	return lockfile.LoadFromPath(path)
}

type captureSetting struct {
	AgentpackID string `json:"agentpack_id"`
	Enabled     bool   `json:"enabled"`
}

// SetCapture enables or disables automatic snapshots for one project directory.
func (s *Store) SetCapture(directory, agentpackID string, enabled bool) error {
	if enabled && !validID(agentpackID) {
		return fmt.Errorf("valid agentpack id is required")
	}
	path, err := captureSettingPath(s.root, directory)
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(captureSetting{AgentpackID: agentpackID, Enabled: true})
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

// CaptureProjectIfEnabled returns without opening or creating registry storage when capture is disabled.
func CaptureProjectIfEnabled(home, definitionRoot, sourceProject, sourceNode, modeName string) (Agentpack, Revision, bool, error) {
	root, err := filepath.Abs(filepath.Join(home, "registry"))
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	path, err := captureSettingPath(root, definitionRoot)
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	transactionHash, err := paths.ProjectPathHash(definitionRoot)
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	transaction := filepath.Join(root, transactionsDir, transactionHash+".json")
	if _, err := os.Lstat(transaction); err == nil {
		store, err := New(home)
		if err != nil {
			return Agentpack{}, Revision{}, false, err
		}
		if err := store.Recover(definitionRoot); err != nil {
			return Agentpack{}, Revision{}, false, err
		}
	} else if !os.IsNotExist(err) {
		return Agentpack{}, Revision{}, false, err
	}
	data, err := readRegular(path)
	if os.IsNotExist(err) {
		return Agentpack{}, Revision{}, false, nil
	}
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	var setting captureSetting
	if err := json.Unmarshal(data, &setting); err != nil || !setting.Enabled || !validID(setting.AgentpackID) {
		return Agentpack{}, Revision{}, false, fmt.Errorf("invalid project capture setting")
	}
	store, err := New(home)
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	input, err := captureProject(definitionRoot)
	if err != nil {
		return Agentpack{}, Revision{}, false, err
	}
	input.ID, input.Project, input.SourceNode, input.Mode = setting.AgentpackID, sourceProject, sourceNode, modeName
	pack, revision, err := store.Capture(input)
	return pack, revision, true, err
}

func captureSettingPath(root, directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("project directory is required")
	}
	hash, err := paths.ProjectPathHash(directory)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, capturesDir, hash+".json"), nil
}
