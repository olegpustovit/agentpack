package acp

import (
	"encoding/json"

	sdkacp "github.com/coder/acp-go-sdk"
)

// InitializeRequest advertises the client capabilities agentpack implements on the worker.
func InitializeRequest() sdkacp.InitializeRequest {
	return sdkacp.InitializeRequest{
		ProtocolVersion: sdkacp.ProtocolVersionNumber,
		ClientCapabilities: sdkacp.ClientCapabilities{
			Fs: sdkacp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
			Elicitation: &sdkacp.ElicitationCapabilities{
				Form: &sdkacp.ElicitationFormCapabilities{},
				Url:  &sdkacp.ElicitationUrlCapabilities{},
			},
		},
	}
}

// AddSessionCapabilities adds standard fields missing from SDK v0.13.5's client capability type.
func AddSessionCapabilities(line []byte) []byte {
	var message map[string]json.RawMessage
	if json.Unmarshal(line, &message) != nil {
		return line
	}
	var method string
	if json.Unmarshal(message["method"], &method) != nil || method != "initialize" {
		return line
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(message["params"], &params) != nil {
		return line
	}
	var capabilities map[string]json.RawMessage
	if raw := params["clientCapabilities"]; len(raw) > 0 && json.Unmarshal(raw, &capabilities) != nil {
		return line
	}
	if capabilities == nil {
		capabilities = make(map[string]json.RawMessage)
	}
	capabilities["session"] = json.RawMessage(`{"configOptions":{"boolean":{}},"notices":{},"compaction":{}}`)
	params["clientCapabilities"], _ = json.Marshal(capabilities)
	message["params"], _ = json.Marshal(params)
	out, err := json.Marshal(message)
	if err != nil {
		return line
	}
	return out
}
