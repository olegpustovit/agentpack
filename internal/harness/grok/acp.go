package grok

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// Grok's ACP agent needs an existing login; the worker authenticates with the advertised "grok.com" method.
func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	ctx.Arguments = []string{"agent", "stdio"}
	home, err := paths.StagingGrokHomeDirForMode(ctx.ProjectRoot, ctx.Mode.Name())
	if err != nil {
		return base.ACPLaunch{}, err
	}
	hasCredentials, err := grokACPCredentialsAvailable(home)
	if err != nil {
		return base.ACPLaunch{}, fmt.Errorf("inspect Grok ACP credentials: %w", err)
	}
	if !hasCredentials {
		return base.ACPLaunch{}, errors.New("Grok ACP requires a stored login; sign in with Grok before starting chat")
	}
	command, err := launch(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	return base.ACPLaunch{Cmd: command, AdaptAgent: adaptAgentLine, AuthenticateMethodID: "grok.com"}, nil
}

func grokACPCredentialsAvailable(home string) (bool, error) {
	shared, err := paths.SharedGrokCredentialsDir()
	if err != nil {
		return false, err
	}
	sharedAuth := filepath.Join(shared, "auth.json")
	if info, err := os.Stat(sharedAuth); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("credential path is not a regular file: %s", sharedAuth)
		}
		if info.Size() > 0 {
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Stat(sharedAuth + ".initialized"); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if info, err := os.Stat(filepath.Join(home, "auth.json")); err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("staged Grok credentials are not a regular file")
		}
		if info.Size() > 0 {
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	candidate, err := newestCredential("auth.json", home)
	if err != nil || candidate == "" {
		return false, err
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && info.Size() > 0, nil
}

func adaptAgentLine(line []byte) base.AgentAdaptResult {
	var envelope map[string]any
	if err := json.Unmarshal(line, &envelope); err != nil {
		return base.AgentAdaptResult{}
	}
	method, _ := envelope["method"].(string)
	if method == "" {
		return base.AgentAdaptResult{}
	}
	if isGrokAuthenticate(method) {
		return base.AgentAdaptResult{}
	}
	if !isGrokExtensionMethod(method) {
		return base.AgentAdaptResult{}
	}
	if !isGrokSessionUpdateMethod(method) {
		return base.AgentAdaptResult{Forward: []byte{}}
	}
	params, ok := envelope["params"].(map[string]any)
	if !ok {
		return base.AgentAdaptResult{Forward: []byte{}}
	}
	out := map[string]any{
		"jsonrpc": envelope["jsonrpc"],
		"method":  "session/update",
		"params":  params,
	}
	if id, ok := envelope["id"]; ok {
		out["id"] = id
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return base.AgentAdaptResult{}
	}
	return base.AgentAdaptResult{Forward: encoded}
}

func isGrokExtensionMethod(method string) bool {
	return strings.HasPrefix(method, "_x.ai/") || strings.HasPrefix(method, "x.ai/")
}

func isGrokAuthenticate(method string) bool {
	return method == "_x.ai/authenticate" || method == "x.ai/authenticate"
}

func isGrokSessionUpdateMethod(method string) bool {
	switch method {
	case "_x.ai/session/update", "x.ai/session/update", "_x.ai/session_notification", "x.ai/session_notification":
		return true
	}
	return strings.HasSuffix(method, "/session/update")
}
