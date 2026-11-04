package omp

import (
	"encoding/json"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const sessionSteeringMethod = "_session/steering"

func acpLaunch(ctx base.LaunchContext) (base.ACPLaunch, error) {
	ctx.Arguments = []string{"acp"}
	command, err := launch(ctx)
	if err != nil {
		return base.ACPLaunch{}, err
	}
	return base.ACPLaunch{Cmd: command, Rewrite: rewriteClientLine}, nil
}

func rewriteClientLine(line []byte) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(line, &envelope); err != nil {
		return line
	}
	method, _ := envelope["method"].(string)
	params, _ := envelope["params"].(map[string]any)
	if params == nil {
		return line
	}
	switch method {
	case sessionSteeringMethod:
		if rewritten := rewriteSteeringToPrompt(envelope, params); rewritten != nil {
			return rewritten
		}
	case "session/prompt":
		if rewritten, ok := mergeSteeringMetaPrompt(envelope, params); ok {
			return rewritten
		}
	}
	return line
}

func rewriteSteeringToPrompt(envelope, params map[string]any) []byte {
	prompt := params["prompt"]
	if prompt == nil {
		if text, ok := params["text"].(string); ok && text != "" {
			prompt = []any{map[string]any{"type": "text", "text": text}}
		}
	}
	if prompt == nil {
		return nil
	}
	outParams := map[string]any{
		"sessionId": params["sessionId"],
		"prompt":    prompt,
	}
	for _, key := range []string{"_meta", "mode", "model"} {
		if v, ok := params[key]; ok {
			outParams[key] = v
		}
	}
	if meta, ok := outParams["_meta"].(map[string]any); ok {
		delete(meta, "steering")
		if len(meta) == 0 {
			delete(outParams, "_meta")
		}
	}
	out := map[string]any{
		"jsonrpc": envelope["jsonrpc"],
		"method":  "session/prompt",
		"params":  outParams,
	}
	if id, ok := envelope["id"]; ok {
		out["id"] = id
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

func mergeSteeringMetaPrompt(envelope, params map[string]any) ([]byte, bool) {
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		return nil, false
	}
	steering, _ := meta["steering"].(map[string]any)
	if steering == nil {
		return nil, false
	}
	text, _ := steering["text"].(string)
	if text == "" {
		if message, ok := steering["message"].(string); ok {
			text = message
		}
	}
	if text == "" {
		return nil, false
	}
	prompt, _ := params["prompt"].([]any)
	merged := append([]any{map[string]any{"type": "text", "text": text}}, prompt...)
	outParams := map[string]any{}
	for k, v := range params {
		outParams[k] = v
	}
	outParams["prompt"] = merged
	delete(meta, "steering")
	if len(meta) == 0 {
		delete(outParams, "_meta")
	}
	out := map[string]any{
		"jsonrpc": envelope["jsonrpc"],
		"method":  "session/prompt",
		"params":  outParams,
	}
	if id, ok := envelope["id"]; ok {
		out["id"] = id
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return encoded, true
}
