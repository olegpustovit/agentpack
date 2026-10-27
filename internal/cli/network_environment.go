package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/paths"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

func recordLaunchEnvironment(packContext paths.Context, selected string, target harness.Target, skipped bool) error {
	id := os.Getenv("AGENTPACK_SESSION_ID")
	if id == "" || os.Getenv("AGENTPACK_SESSION_WORKER") != "1" {
		return nil
	}
	home, err := paths.UserAgentpackHome()
	if err != nil {
		return err
	}
	stamp, found, err := packSync.ReadLaunchDigest(packContext.DefinitionRoot, selected, target)
	if err != nil {
		return err
	}
	workspace, err := paths.ProjectPathHash(packContext.WorkspaceRoot)
	if err != nil {
		return err
	}
	cache, err := paths.ProjectStateFile(packContext.DefinitionRoot, "runtime-environment-"+workspace+"-"+paths.ModePathComponent(selected)+"-"+string(target)+".json")
	if err != nil {
		return err
	}
	var state struct {
		Stamp  string `json:"stamp"`
		Digest string `json:"digest"`
	}
	if skipped && found && stamp != "" {
		data, err := os.ReadFile(cache)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("read runtime environment cache: %w", err)
			}
			decoded, err := hex.DecodeString(state.Digest)
			if state.Stamp == stamp && err == nil && len(decoded) == 32 {
				return network.RecordSessionEnvironment(home, id, state.Digest)
			}
		}
	}
	state.Digest, err = packSync.EnvironmentDigest(packContext.DefinitionRoot, packContext.WorkspaceRoot, selected, target)
	if err != nil {
		return err
	}
	if found && stamp != "" {
		state.Stamp = stamp
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		// Effective inputs include MCP credentials; keep their cached hash private.
		if err := network.EnsurePrivateDirectory(filepath.Dir(cache)); err != nil {
			return err
		}
		if err := os.WriteFile(cache+".tmp", data, 0600); err != nil {
			return err
		}
		if err := os.Rename(cache+".tmp", cache); err != nil {
			return err
		}
	}
	return network.RecordSessionEnvironment(home, id, state.Digest)
}
