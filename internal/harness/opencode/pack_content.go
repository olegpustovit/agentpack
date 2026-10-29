package opencode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olegpustovit/agentpack/internal/artifacts"
	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/hooks"
	"github.com/olegpustovit/agentpack/internal/mcp"
)

func finalizeSources(entries mcp.Entries, ctx base.StageContext) error {
	major := nativeMajor()
	if major != 1 && major != 2 {
		return nil
	}
	root, err := stagedRoot(ctx)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		path := packConfigPath(root)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var generated map[string]any
		if err := json.Unmarshal(data, &generated); err != nil {
			return err
		}
		instructions, _ := generated["instructions"].([]any)
		generated["instructions"] = append(instructions, "AGENTS.md")
		if err := writeConfig(path, generated); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	content := map[string]any{}
	for _, kind := range []string{"agents", "commands"} {
		items := map[string]any{}
		directory := filepath.Join(root, kind)
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			artifact, err := artifacts.Parse(relative, string(data), "")
			if err != nil || artifact == nil {
				return err
			}
			value := map[string]any{"description": artifact.Description}
			for key, extra := range artifact.ExtraFrontmatter {
				value[key] = extra
			}
			name := artifact.Name
			if kind == "agents" {
				value["prompt"] = strings.TrimSpace(artifact.Body)
			} else {
				name = strings.TrimSuffix(filepath.ToSlash(strings.TrimPrefix(relative, kind+string(filepath.Separator))), ".md")
				value["template"] = strings.TrimSpace(artifact.Body)
			}
			if major == 2 {
				field := "prompt"
				if kind == "commands" {
					field = "template"
				}
				body := value[field].(string)
				digest := sha256.Sum256([]byte(body))
				bodyPath := filepath.Join(root, "agentpack-hooks", "content", fmt.Sprintf("%x.txt", digest))
				if !strings.Contains(bodyPath, "}") {
					if err := os.MkdirAll(filepath.Dir(bodyPath), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(bodyPath, []byte(body), 0o644); err != nil {
						return err
					}
					value[field] = "{file:" + bodyPath + "}"
				}
			}
			items[name] = value
			return nil
		})
		if err != nil {
			return fmt.Errorf("read staged OpenCode %s: %w", kind, err)
		}
		if len(items) != 0 {
			content[kind] = items
		}
	}
	if info, err := os.Stat(filepath.Join(root, "skills")); err == nil && info.IsDir() {
		content["skillsPath"] = filepath.Join(root, "skills")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if major == 2 {
		path := packConfigPath(root)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var generated map[string]any
		if err := json.Unmarshal(data, &generated); err != nil {
			return err
		}
		if agents := content["agents"]; agents != nil {
			generated["agent"] = agents
		}
		if commands := content["commands"]; commands != nil {
			generated["command"] = commands
		}
		if skills := content["skillsPath"]; skills != nil {
			generated["skills"] = map[string]any{"paths": []any{skills}}
		}
		return writeConfig(path, generated)
	}
	pluginRoot := filepath.Join(root, "agentpack-hooks")
	configPath := filepath.Join(pluginRoot, "config.json")
	config := map[string]any{"agentpack": hooks.Executable(), "hooks": []any{}}
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("read OpenCode hook config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if len(content) == 0 {
		return nil
	}
	for key, value := range content {
		config[key] = value
	}
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	index := filepath.Join(pluginRoot, "index.js")
	if _, err := os.Stat(index); os.IsNotExist(err) {
		if err := os.WriteFile(index, []byte(pluginSource), 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	generated, err := mergedConfig(root, "./agentpack-hooks/index.js")
	if err != nil {
		return err
	}
	return writeConfig(packConfigPath(root), generated)
}
