package omp

import (
	"os"
	"path/filepath"
)

// ConfigInputs lists native configuration that can change discovery, excluding
// credentials and runtime databases. OMP continues to load these files itself.
func ConfigInputs(workspace string) []string {
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	config := os.Getenv("PI_CONFIG_DIR")
	if config == "" {
		config = ".omp"
	}
	root := filepath.Join(home, config)
	profile, present := os.LookupEnv("OMP_PROFILE")
	if !present {
		profile = os.Getenv("PI_PROFILE")
	}
	if profile != "" && filepath.Base(profile) == profile {
		root = filepath.Join(root, "profiles", profile)
	}
	userRoots := []string{filepath.Join(root, "agent")}
	if profile == "" {
		if explicit := os.Getenv("PI_CODING_AGENT_DIR"); explicit != "" {
			if !filepath.IsAbs(explicit) {
				explicit = filepath.Join(workspace, explicit)
			}
			userRoots = append(userRoots, explicit)
		}
	}
	userRoots = append(userRoots, filepath.Join(home, ".claude"), filepath.Join(home, ".codex"), filepath.Join(home, ".agents"))
	var inputs []string
	add := func(root string) {
		for _, name := range []string{"config.yml", "config.yaml", "settings.json", "models.yml", "models.yaml", "mcp.json", ".mcp.json", "lsp.json", ".lsp.json", "AGENTS.md", "RULES.md", "SYSTEM.md", "SYSTEM_TEMPLATE.md", "APPEND_SYSTEM.md", "skills", "agents", "commands", "rules", "hooks", "extensions", "tools", "prompts", "instructions"} {
			inputs = append(inputs, filepath.Join(root, name))
		}
	}
	for _, root := range userRoots {
		add(root)
	}
	for directory := workspace; ; directory = filepath.Dir(directory) {
		add(filepath.Join(directory, ".omp"))
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil || directory == home || filepath.Dir(directory) == directory {
			break
		}
	}
	for _, path := range filepath.SplitList(os.Getenv("PI_CONFIG_FILES")) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		inputs = append(inputs, path)
	}
	return inputs
}
