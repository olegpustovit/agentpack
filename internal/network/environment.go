package network

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	packSync "github.com/olegpustovit/agentpack/internal/sync"
)

// Environment is what a session launched with: the selected content's digest, the names of what it
// selects, and the launch fingerprint it was computed under. It names no configuration or secret.
type Environment struct {
	Digest string   `json:"digest"`
	Items  []string `json:"items,omitempty"`
	Stamp  string   `json:"stamp,omitempty"`
	// Binary is the running executable's tag: the digest's algorithm is that binary's staging code.
	Binary string `json:"binary"`
}

func environmentPath(home, id string) (string, error) {
	if !ValidSessionID(id) {
		return "", errors.New("invalid session ID")
	}
	return filepath.Join(home, "network", "sessions", id, "environment"), nil
}

// BinaryTag identifies the running agentpack executable. Environment digests depend on staging
// code, so a digest is only comparable with one computed by the same binary; dev builds of a
// dirty tree share a commit, so the file's own identity is used.
func BinaryTag() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(executable)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", executable, info.Size(), info.ModTime().UnixNano())
}

func readEnvironment(path string, matchBinary bool) (Environment, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Environment{}, nil
	}
	if err != nil {
		return Environment{}, err
	}
	var environment Environment
	if json.Unmarshal(data, &environment) != nil || matchBinary && (environment.Binary == "" || environment.Binary != BinaryTag()) {
		return Environment{}, nil
	}
	if decoded, err := hex.DecodeString(environment.Digest); err != nil || len(decoded) != 32 {
		return Environment{}, errors.New("invalid session environment fingerprint")
	}
	return environment, nil
}

func writeEnvironment(path string, environment Environment) error {
	environment.Binary = BinaryTag()
	data, err := json.Marshal(environment)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// launchEnvironment is the project, mode and agent a session runs, which its environment is computed for.
type launchEnvironment struct {
	home, id, definition, workspace, mode string
	target                                base.Target
}

// current computes the environment from the launch fingerprint, reusing the project's cached
// digest while the fingerprint is unchanged, so only a real change rehashes the selected content.
func (launch launchEnvironment) current() (Environment, error) {
	stamp, err := packSync.LaunchFingerprint(launch.definition, launch.workspace, launch.mode, launch.target)
	if err != nil {
		return Environment{}, err
	}
	workspace, err := paths.ProjectPathHash(launch.workspace)
	if err != nil {
		return Environment{}, err
	}
	cache, err := paths.ProjectStateFile(launch.definition, "runtime-environment-"+workspace+"-"+paths.ModePathComponent(launch.mode)+"-"+string(launch.target)+".json")
	if err != nil {
		return Environment{}, err
	}
	if cached, err := readEnvironment(cache, true); err != nil || cached.Stamp == stamp && cached.Digest != "" {
		return cached, err
	}
	environment := Environment{Stamp: stamp}
	if environment.Digest, environment.Items, err = packSync.SelectedEnvironment(launch.definition, launch.workspace, launch.mode, launch.target); err != nil {
		return Environment{}, err
	}
	// The digest depends on MCP connections; keep it private.
	if err := paths.EnsurePrivateDirectory(filepath.Dir(cache)); err != nil {
		return Environment{}, err
	}
	return environment, writeEnvironment(cache, environment)
}

// record stores the environment the session launches with and returns the one it replaces.
func (launch launchEnvironment) record() (previous, current Environment, err error) {
	path, err := environmentPath(launch.home, launch.id)
	if err != nil {
		return Environment{}, Environment{}, err
	}
	if previous, err = readEnvironment(path, true); err != nil {
		return Environment{}, Environment{}, err
	}
	if current, err = launch.current(); err != nil {
		return previous, Environment{}, err
	}
	return previous, current, writeEnvironment(path, current)
}

// stale reports whether the session's environment moved since it launched. It hashes the selected
// content only when the launch fingerprint moved, and records the new fingerprint when the content did not.
func (launch launchEnvironment) stale() (bool, error) {
	path, err := environmentPath(launch.home, launch.id)
	if err != nil {
		return false, err
	}
	recorded, err := readEnvironment(path, true)
	if err != nil || recorded.Digest == "" {
		return false, err
	}
	stamp, err := packSync.LaunchFingerprint(launch.definition, launch.workspace, launch.mode, launch.target)
	if err != nil || stamp == recorded.Stamp {
		return false, err
	}
	current, err := launch.current()
	if err != nil {
		return false, err
	}
	if current.Digest != recorded.Digest {
		return true, nil
	}
	recorded.Stamp = current.Stamp
	return false, writeEnvironment(path, recorded)
}

// RecordLaunchEnvironment records what a session worker's agent launched with.
func RecordLaunchEnvironment(home, id, definitionRoot, workspace, selectedMode string, target base.Target) error {
	_, _, err := launchEnvironment{home: home, id: id, definition: definitionRoot, workspace: workspace, mode: selectedMode, target: target}.record()
	return err
}

// reloadNotice is the one line a reloaded chat shows: what its agent gained and lost.
func reloadNotice(before, after []string) string {
	var changes []string
	for _, item := range after {
		if !slices.Contains(before, item) {
			if !strings.HasSuffix(item, " connected") {
				item = "+" + item
			}
			changes = append(changes, item)
		}
	}
	for _, item := range before {
		if !slices.Contains(after, item) {
			if name, connection := strings.CutSuffix(item, " connected"); connection {
				item = name + " disconnected"
			} else {
				item = "−" + item
			}
			changes = append(changes, item)
		}
	}
	if len(changes) == 0 {
		return "Reloaded"
	}
	return "Reloaded: " + strings.Join(changes, ", ")
}

// restartable: chats (ACP) and browser terminals; native-stdio presence has no worker to restart.
func restartable(info session.SessionInfo) bool {
	return (info.Transport == "acp" || info.Transport != "shell" && info.State.Interactive) &&
		info.State.Running && !info.State.Restarting
}

// Reload is a session RestartStaleSessions asked to reload; Deferred means it waits until the agent is idle.
type Reload struct {
	Harness, Workspace, Mode string
	Deferred                 bool
}

// RestartStaleSessions reloads this node's running sessions of definitionRoot ("" for every
// project) whose environment differs from the one they launched with: a chat at its next idle
// moment, a terminal once its agent is idle. Each is staged and validated before any restarts.
// sessions are this node's live sessions (Sessions or SessionsIn).
func RestartStaleSessions(ctx context.Context, home string, service packSync.Service, definitionRoot string, sessions []session.SessionInfo) ([]Reload, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	var root os.FileInfo
	var err error
	if definitionRoot != "" {
		if root, err = os.Stat(definitionRoot); err != nil {
			return nil, err
		}
	}
	byID := map[string]session.SessionInfo{}
	var before []packSync.RunningEnvironment
	for _, info := range sessions {
		if !restartable(info) {
			continue
		}
		definition := cmp.Or(info.DefinitionRoot, info.Directory)
		if root != nil {
			if stat, err := os.Stat(definition); err != nil || !os.SameFile(root, stat) {
				continue
			}
		}
		target, err := HarnessTarget(info.Harness)
		if err != nil {
			return nil, err
		}
		path, err := environmentPath(home, info.ID)
		if err != nil {
			return nil, err
		}
		// A deliberate project change can also reach workers already running across an update.
		recorded, err := readEnvironment(path, root == nil)
		if err != nil {
			return nil, fmt.Errorf("check running session %s: %w", info.ID, err)
		}
		if recorded.Digest == "" {
			continue // launched by another binary, or still starting; its next launch records one
		}
		byID[info.ID] = info
		before = append(before, packSync.RunningEnvironment{SessionID: info.ID, DefinitionRoot: definition, Workspace: info.Directory, Mode: info.Mode, Target: target, Digest: recorded.Digest})
	}
	changed, err := service.ChangedEnvironments(ctx, before)
	if err != nil {
		return nil, err
	}
	var reloads []Reload
	var failures []error
	for _, environment := range changed {
		deferred, err := RestartWorkerWhenIdle(ctx, home, environment.SessionID, rand.Text())
		if err != nil {
			failures = append(failures, fmt.Errorf("change saved, but session %s could not restart: %w", environment.SessionID, err))
			continue
		}
		reloads = append(reloads, Reload{Harness: byID[environment.SessionID].Harness, Workspace: environment.Workspace, Mode: environment.Mode, Deferred: deferred})
	}
	return reloads, errors.Join(failures...)
}
