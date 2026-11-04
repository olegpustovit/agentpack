package hub

import (
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"strings"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func formatThread(state acp.ThreadState, width int) []string {
	if width < 20 {
		width = 80
	}
	contentWidth := max(1, width-4)
	var lines []string
	if state.NeedsYou {
		appendWrapped(&lines, "◆ Needs you", contentWidth)
	}
	if state.Working {
		appendWrapped(&lines, "… turn running", contentWidth)
	}
	if state.PlanKind != "" {
		appendWrapped(&lines, "◇ Plan · "+state.PlanKind, contentWidth)
	}
	for i := 0; i < len(state.Items); {
		if exploratoryTool(state.Items[i]) {
			j := i + 1
			for j < len(state.Items) && exploratoryTool(state.Items[j]) {
				j++
			}
			if j-i > 1 {
				appendWrapped(&lines, fmt.Sprintf("▸ Explored %d steps", j-i), contentWidth)
				i = j
				continue
			}
		}
		line := threadLine(state.Items[i])
		if line != "" {
			appendWrapped(&lines, line, contentWidth)
		}
		i++
	}
	if len(lines) == 0 {
		lines = append(lines, "(no messages yet)")
	}
	return lines
}

func appendWrapped(lines *[]string, text string, width int) {
	text = acp.StripControls(text)
	for _, paragraph := range strings.Split(text, "\n") {
		wrapped := ansi.WrapWc(paragraph, width, " ")
		*lines = append(*lines, strings.Split(wrapped, "\n")...)
	}
}

func exploratoryTool(item acp.ThreadItem) bool {
	if item.Kind != "tool" {
		return false
	}
	var tool struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(item.Data, &tool)
	return tool.Kind == "read" || tool.Kind == "search" || tool.Kind == "list"
}

func threadLine(item acp.ThreadItem) string {
	switch item.Kind {
	case "agent_message_chunk", "user_message_chunk", "agent_thought_chunk":
		text := chunkText(item.Data)
		if text == "" {
			return ""
		}
		switch item.Kind {
		case "user_message_chunk":
			return "▷ " + text
		case "agent_thought_chunk":
			return "· " + text
		default:
			return text
		}
	case "tool":
		var tool struct {
			Title  string `json:"title"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(item.Data, &tool)
		label := tool.Title
		if label == "" {
			label = tool.Kind
		}
		if label == "" {
			label = "tool"
		}
		return fmt.Sprintf("▸ %s (%s)", label, tool.Status)
	case "permission":
		var perm struct {
			Tool    string `json:"tool"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(item.Data, &perm)
		if perm.Summary != "" {
			return fmt.Sprintf("! %s: %s", perm.Tool, perm.Summary)
		}
		return "! " + perm.Tool
	case "elicitation":
		var ask struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(item.Data, &ask)
		if ask.Prompt != "" {
			return "? " + ask.Prompt
		}
		return "? input needed"
	case "divider":
		var d struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(item.Data, &d)
		if d.Message != "" {
			return "— " + d.Message + " —"
		}
		return "—"
	default:
		return ""
	}
}

func chunkText(raw json.RawMessage) string {
	var body struct {
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	var flat string
	if json.Unmarshal(body.Text, &flat) == nil {
		return flat
	}
	var nested struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(body.Text, &nested)
	return nested.Text
}

func clipLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func previewWindow(lines []string, maxRows, tailFromBottom int) []string {
	if maxRows < 1 || len(lines) <= maxRows {
		return lines
	}
	if tailFromBottom < 0 {
		tailFromBottom = 0
	}
	maxTail := len(lines) - maxRows
	if tailFromBottom > maxTail {
		tailFromBottom = maxTail
	}
	end := len(lines) - tailFromBottom
	start := end - maxRows
	return lines[start:end]
}
