package grok

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/paths"
)

const homesDir = "grok-homes"

// StagingHome is the durable companion of the selected staging generation.
// The bundle and its home share the staging pointer, lease and launch record.
func StagingHome(projectRoot, modeName string) (string, error) {
	name, err := paths.StagingGenerationName(projectRoot, modeName)
	if err != nil {
		return "", err
	}
	return paths.ProjectStateFile(projectRoot, homesDir, paths.ModePathComponent(modeName), name)
}

// RetireStagingHome runs under the retired staging generation's exclusive
// lease. Keep homes with credentials that may still need recovery.
func RetireStagingHome(projectRoot, modeName, stageRoot string) bool {
	home, err := paths.ProjectStateFile(projectRoot, homesDir, paths.ModePathComponent(modeName), filepath.Base(stageRoot))
	if err != nil {
		return false
	}
	shared, err := sharedCredentialsDir()
	if err != nil {
		return false
	}
	return !credentialsMirrored(home, shared) || os.RemoveAll(home) == nil
}

func credentialsMirrored(home, shared string) bool {
	for _, name := range credentialFiles {
		local, global := filepath.Join(home, name), filepath.Join(shared, name)
		info, err := os.Lstat(local)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		info, err = os.Lstat(global)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		left, localErr := os.ReadFile(local)
		right, sharedErr := os.ReadFile(global)
		if localErr != nil || sharedErr != nil || !bytes.Equal(left, right) {
			return false
		}
	}
	return true
}
