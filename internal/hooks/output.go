package hooks

// GuidanceHookSpecific is the answer Claude Code reads from the inject-guidance hook.
func GuidanceHookSpecific(body string, event Event) map[string]any {
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": string(event), "additionalContext": body}}
}

// DefaultOutput is agentpack's own hook answer, read by agents whose renderer routes hooks
// through an agentpack-written adapter (Claude, OMP).
func DefaultOutput(result Result) any {
	metadata := result.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return map[string]any{"decision": result.Decision, "message": nullable(result.Message), "additional_context": nullable(result.AdditionalContext), "updated_input": result.UpdatedInput, "updated_tool_output": result.UpdatedToolOutput, "metadata": metadata}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
