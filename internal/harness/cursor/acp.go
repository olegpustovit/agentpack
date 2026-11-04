package cursor

import (
	"encoding/json"
	"strings"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

var cursorExtensionKinds = map[string]struct{}{
	"ask_question":   {},
	"create_plan":    {},
	"update_todos":   {},
	"task":           {},
	"generate_image": {},
}

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	ctx.Arguments = []string{"acp"}
	command, err := launch(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	return base.ACPLaunch{Cmd: command, AdaptAgent: adaptAgentLine}, nil
}

func adaptAgentLine(line []byte) base.AgentAdaptResult {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return base.AgentAdaptResult{}
	}
	if !strings.HasPrefix(envelope.Method, "cursor/") {
		return base.AgentAdaptResult{}
	}
	kind := strings.TrimPrefix(envelope.Method, "cursor/")
	if _, ok := cursorExtensionKinds[kind]; !ok {
		return base.AgentAdaptResult{Forward: []byte{}}
	}
	payload := envelope.Params
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	result := base.AgentAdaptResult{
		Forward: []byte{},
		Log:     []base.LogKind{{Kind: kind, Payload: payload}},
	}
	if (kind == "ask_question" || kind == "create_plan") && len(envelope.ID) > 0 && string(envelope.ID) != "null" {
		// The SDK handles extension requests only in ACP's underscore namespace.
		envelope.Method = "_agentpack/cursor/" + kind
		result.Forward, _ = json.Marshal(envelope)
	}
	return result
}
