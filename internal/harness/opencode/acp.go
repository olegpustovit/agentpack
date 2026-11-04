package opencode

import (
	"encoding/json"

	base "github.com/olegpustovit/agentpack/internal/harness"
)

const childSessionUpdatesMeta = "opencode/child-session-updates"

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
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil || envelope.Method != "session/update" {
		return base.AgentAdaptResult{}
	}
	meta, _ := envelope.Params["_meta"].(map[string]any)
	if meta == nil {
		return base.AgentAdaptResult{}
	}
	raw, ok := meta[childSessionUpdatesMeta]
	if !ok {
		return base.AgentAdaptResult{}
	}
	items, ok := raw.([]any)
	if !ok {
		return base.AgentAdaptResult{Forward: []byte{}}
	}
	var log []base.LogKind
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		groupID := groupedToolID(row)
		if groupID == "" {
			continue
		}
		payload := map[string]any{}
		for k, v := range row {
			payload[k] = v
		}
		payload["toolCallId"] = groupID
		encoded, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		log = append(log, base.LogKind{Kind: "tool_call", Payload: encoded})
	}
	delete(meta, childSessionUpdatesMeta)
	if len(meta) == 0 {
		delete(envelope.Params, "_meta")
	}
	forward := line
	if len(log) > 0 {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return base.AgentAdaptResult{Log: log, Forward: []byte{}}
		}
		forward = encoded
	}
	return base.AgentAdaptResult{Forward: forward, Log: log}
}

func groupedToolID(row map[string]any) string {
	for _, key := range []string{"groupId", "groupedId", "groupedToolCallId"} {
		if id, _ := row[key].(string); id != "" {
			return id
		}
	}
	if id, _ := row["toolCallId"].(string); id != "" {
		return id
	}
	return ""
}
