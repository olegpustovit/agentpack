package hub

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/olegpustovit/agentpack/internal/acp"
)

// formatChatThread renders the ACP thread as plain terminal text.
func formatChatThread(state acp.ThreadState, width int, expanded map[string]bool) []string {
	return renderChatThread(state, width, expanded, nil, nil, nil)
}

// chatToolGroupAtLine returns the expansion key for a group header row.
func chatToolGroupAtLine(state acp.ThreadState, width, line int, expanded map[string]bool) string {
	if line < 0 {
		return ""
	}
	groupLines := make(map[int]string)
	renderChatThread(state, width, expanded, groupLines, nil, nil)
	return groupLines[line]
}

func chatImageAtLine(state acp.ThreadState, width, line int, expanded map[string]bool) chatImageRef {
	if line < 0 {
		return chatImageRef{}
	}
	imageLines := make(map[int]chatImageRef)
	renderChatThread(state, width, expanded, nil, imageLines, nil)
	return imageLines[line]
}

func chatMessageAtLine(state acp.ThreadState, width, line int, expanded map[string]bool) string {
	if line < 0 {
		return ""
	}
	messageLines := make(map[int]string)
	renderChatThread(state, width, expanded, nil, nil, messageLines)
	return messageLines[line]
}

type chatLineActions struct {
	expandable string
	image      chatImageRef
	message    string
}

func chatActionsAtLine(state acp.ThreadState, width, line int, expanded map[string]bool) chatLineActions {
	if line < 0 {
		return chatLineActions{}
	}
	groupLines := make(map[int]string)
	imageLines := make(map[int]chatImageRef)
	messageLines := make(map[int]string)
	renderChatThread(state, width, expanded, groupLines, imageLines, messageLines)
	return chatLineActions{
		expandable: groupLines[line],
		image:      imageLines[line],
		message:    messageLines[line],
	}
}

func chatFirstExpandable(state acp.ThreadState, width int, expanded map[string]bool) string {
	groupLines := make(map[int]string)
	lines := renderChatThread(state, width, expanded, groupLines, nil, nil)
	for line := range lines {
		if key := groupLines[line]; key != "" {
			return key
		}
	}
	return ""
}

func renderChatThread(state acp.ThreadState, width int, expanded map[string]bool, groupLines map[int]string, imageLines map[int]chatImageRef, messageLines map[int]string) []string {
	if width < 1 {
		width = 80
	}
	lines := make([]string, 0, len(state.Items)*2+2)
	if state.NeedsYou {
		appendChatWrapped(&lines, "◆ Needs you", width)
	}
	if state.Working {
		appendChatWrapped(&lines, "… turn running", width)
	}

	hasPlan := false
	for _, item := range state.Items {
		if item.Kind == "plan" {
			hasPlan = true
			break
		}
	}
	if state.PlanKind != "" && !hasPlan {
		appendChatWrapped(&lines, "◇ Plan · "+state.PlanKind, width)
	}

	for i := 0; i < len(state.Items); {
		item := state.Items[i]
		switch item.Kind {
		case "agent_thought_chunk":
			end := i + 1
			var thought strings.Builder
			thought.WriteString(chunkText(item.Data))
			for end < len(state.Items) && state.Items[end].Kind == "agent_thought_chunk" {
				thought.WriteString(chunkText(state.Items[end].Data))
				end++
			}
			if text := thought.String(); text != "" {
				key := chatThoughtKey(i)
				firstLine := len(lines)
				marker := "▸"
				if chatExpandableOpen(key, expanded) {
					marker = "▾"
				}
				appendChatWrapped(&lines, marker+" Thought", width)
				recordExpandableLines(groupLines, firstLine, len(lines), key)
				if chatExpandableOpen(key, expanded) {
					appendChatBlock(&lines, "│ ", text, width)
				}
			}
			i = end
			continue
		case "agent_message_chunk", "user_message_chunk":
			end := i + 1
			var message strings.Builder
			message.WriteString(chunkText(item.Data))
			for end < len(state.Items) && state.Items[end].Kind == item.Kind {
				message.WriteString(chunkText(state.Items[end].Data))
				end++
			}
			text := message.String()
			if text != "" {
				firstLine := len(lines)
				if item.Kind == "user_message_chunk" {
					appendChatBlock(&lines, "┃  ", text, width)
				} else {
					appendChatBlock(&lines, "", text, width)
				}
				if messageLines != nil {
					for line := firstLine; line < len(lines); line++ {
						messageLines[line] = cleanChatText(text)
					}
				}
			}
			for j := i; j < end; j++ {
				appendChatImageLines(&lines, state.Items[j].Data, width, imageLines)
			}
			i = end
			continue
		case "tool":
			tool := decodeChatTool(item.Data)
			if exploratoryToolKind(tool.Kind) {
				end, counts := exploratoryRun(state.Items, i)
				if end-i > 1 {
					key := chatToolGroupKey(i)
					firstLine := len(lines)
					marker := "▸"
					if chatExpandableOpen(key, expanded) {
						marker = "▾"
					}
					label := exploratoryLabel(end-i, counts)
					label = marker + strings.TrimPrefix(label, "▸")
					appendChatWrapped(&lines, label, width)
					recordExpandableLines(groupLines, firstLine, len(lines), key)
					if chatExpandableOpen(key, expanded) {
						for j := i; j < end; j++ {
							appendChatTool(&lines, decodeChatTool(state.Items[j].Data), width)
						}
					}
					i = end
					continue
				}
			}
			key := chatToolKey(i)
			open := chatExpandableOpen(key, expanded)
			firstLine := len(lines)
			appendChatToolHeader(&lines, tool, width, open)
			if chatToolHasDetails(tool) {
				recordExpandableLines(groupLines, firstLine, len(lines), key)
				if open {
					appendChatToolDetails(&lines, tool, width)
				}
			}
		case "plan":
			appendChatPlan(&lines, item.Data, width)
		case "permission":
			appendChatPermission(&lines, item.Data, width)
		case "elicitation":
			appendChatElicitation(&lines, item.Data, width)
		case "compaction":
			var compaction struct {
				Status  string `json:"status"`
				Summary string `json:"summary"`
				Error   string `json:"error"`
			}
			_ = json.Unmarshal(item.Data, &compaction)
			label := "Compaction"
			switch compaction.Status {
			case "in_progress":
				label = "Compaction in progress"
			case "completed":
				label = "Conversation compacted"
			case "failed":
				label = "Compaction failed"
			case "cancelled":
				label = "Compaction cancelled"
			default:
				if compaction.Status != "" {
					label += " · " + compaction.Status
				}
			}
			appendChatWrapped(&lines, "— "+label+" —", width)
			if compaction.Summary != "" {
				appendChatBlock(&lines, "│ ", compaction.Summary, width)
			}
			if compaction.Error != "" {
				appendChatBlock(&lines, "│ ", compaction.Error, width)
			}
		case "divider":
			var divider struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(item.Data, &divider)
			if divider.Message != "" {
				appendChatWrapped(&lines, "— "+divider.Message+" —", width)
			}
		}
		i++
	}
	if len(lines) == 0 {
		lines = append(lines, "(no messages yet)")
	}
	return lines
}

type chatToolContent struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	Text    string `json:"text"`
}

type chatToolLocation struct {
	Path string `json:"path"`
}

type chatToolData struct {
	ToolCallID string             `json:"toolCallId"`
	Title      string             `json:"title"`
	Kind       string             `json:"kind"`
	Status     string             `json:"status"`
	Command    string             `json:"command"`
	Output     string             `json:"output"`
	Content    []chatToolContent  `json:"content"`
	Locations  []chatToolLocation `json:"locations"`
}

func decodeChatTool(data json.RawMessage) chatToolData {
	var tool chatToolData
	_ = json.Unmarshal(data, &tool)
	return tool
}

func exploratoryToolKind(kind string) bool {
	return kind == "read" || kind == "search" || kind == "list"
}

func exploratoryRun(items []acp.ThreadItem, start int) (int, [3]int) {
	var counts [3]int
	end := start
	for end < len(items) && items[end].Kind == "tool" {
		kind := decodeChatTool(items[end].Data).Kind
		index := -1
		switch kind {
		case "read":
			index = 0
		case "search":
			index = 1
		case "list":
			index = 2
		default:
			return end, counts
		}
		counts[index]++
		end++
	}
	return end, counts
}

func chatToolGroupKey(firstItemIndex int) string {
	return fmt.Sprintf("tools:%d", firstItemIndex)
}

func chatThoughtKey(firstItemIndex int) string {
	return fmt.Sprintf("thought:%d", firstItemIndex)
}

func chatToolKey(firstItemIndex int) string {
	return fmt.Sprintf("tool:%d", firstItemIndex)
}

func chatExpandableOpen(key string, expanded map[string]bool) bool {
	if open, ok := expanded[key]; ok {
		return open
	}
	return strings.HasPrefix(key, "tool:")
}

func recordExpandableLines(lineKeys map[int]string, start, end int, key string) {
	if lineKeys == nil {
		return
	}
	for line := start; line < end; line++ {
		lineKeys[line] = key
	}
}

func exploratoryLabel(steps int, counts [3]int) string {
	label := fmt.Sprintf("▸ Explored %d steps", steps)
	parts := make([]string, 0, 3)
	for i, name := range [...]string{"read", "search", "list"} {
		if counts[i] == 0 {
			continue
		}
		if counts[i] != 1 {
			name += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", counts[i], name))
	}
	if len(parts) > 0 {
		label += " · " + strings.Join(parts, ", ")
	}
	return label
}

func chatToolTitle(tool chatToolData) string {
	if tool.Title != "" {
		return tool.Title
	}
	if tool.Kind != "" {
		return tool.Kind
	}
	if tool.Output != "" {
		return "Terminal"
	}
	return "Tool"
}

func chatToolHasDetails(tool chatToolData) bool {
	return tool.Command != "" || tool.Output != "" || len(tool.Locations) > 0 || len(tool.Content) > 0
}

func appendChatToolHeader(lines *[]string, tool chatToolData, width int, expanded bool) {
	marker := "•"
	switch tool.Status {
	case "completed", "succeeded":
		marker = "✓"
	case "in_progress":
		marker = "◐"
	case "failed":
		marker = "!"
	}
	toggle := "  "
	if chatToolHasDetails(tool) {
		toggle = "▸ "
		if expanded {
			toggle = "▾ "
		}
	}
	header := toggle + marker + " " + chatToolTitle(tool)
	if tool.Status != "" {
		header += " · " + tool.Status
	}
	appendChatWrapped(lines, header, width)
}

func appendChatToolDetails(lines *[]string, tool chatToolData, width int) {
	title := chatToolTitle(tool)
	if tool.Command != "" {
		appendChatBlock(lines, "  $ ", tool.Command, width)
	}
	for _, location := range tool.Locations {
		if location.Path != "" {
			appendChatBlock(lines, "  ↳ ", location.Path, width)
		}
	}
	for _, content := range tool.Content {
		switch content.Type {
		case "diff":
			path := content.Path
			if path == "" {
				path = title
			}
			appendChatWrapped(lines, "  Edit "+path, width)
			appendChatDiff(lines, "  - ", content.OldText, width)
			appendChatDiff(lines, "  + ", content.NewText, width)
		case "text":
			if content.Text != "" {
				appendChatBlock(lines, "  ", content.Text, width)
			}
		}
	}
	if tool.Output != "" {
		appendChatBlock(lines, "│ ", tool.Output, width)
	}
}

func appendChatTool(lines *[]string, tool chatToolData, width int) {
	appendChatToolHeader(lines, tool, width, true)
	appendChatToolDetails(lines, tool, width)
}

func appendChatDiff(lines *[]string, prefix, text string, width int) {
	if text == "" {
		return
	}
	text = strings.TrimSuffix(text, "\n")
	for _, line := range strings.Split(text, "\n") {
		appendChatBlock(lines, prefix, line, width)
	}
}

func appendChatPlan(lines *[]string, data json.RawMessage, width int) {
	var plan struct {
		Entries []struct {
			Status  string `json:"status"`
			Content string `json:"content"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(data, &plan)
	done := 0
	for _, entry := range plan.Entries {
		if entry.Status == "completed" || entry.Status == "done" {
			done++
		}
	}
	appendChatWrapped(lines, fmt.Sprintf("Plan · %d/%d complete", done, len(plan.Entries)), width)
	for _, entry := range plan.Entries {
		marker := "☐"
		switch entry.Status {
		case "completed", "done":
			marker = "☑"
		case "in_progress", "run":
			marker = "◐"
		}
		appendChatBlock(lines, marker+" ", entry.Content, width)
	}
}

func appendChatPermission(lines *[]string, data json.RawMessage, width int) {
	var permission struct {
		Tool     string `json:"tool"`
		Summary  string `json:"summary"`
		Command  string `json:"command"`
		Resolved bool   `json:"resolved"`
		Inactive bool   `json:"inactive"`
		Result   struct {
			OptionID string `json:"optionId"`
		} `json:"result"`
		Options []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	}
	_ = json.Unmarshal(data, &permission)
	boxWidth := min(width, 72)
	if boxWidth < 8 {
		boxWidth = width
	}
	appendChatWrapped(lines, "┏"+strings.Repeat("━", max(0, boxWidth-2))+"┓", width)
	title := "Permission required"
	if permission.Resolved {
		title = "Permission answered"
	} else if permission.Inactive {
		title = "Permission no longer active"
	}
	if permission.Tool != "" {
		title += " · " + permission.Tool
	}
	appendChatCardText(lines, title, boxWidth)
	if permission.Summary != "" {
		appendChatCardText(lines, permission.Summary, boxWidth)
	}
	if permission.Command != "" {
		appendChatCardText(lines, "$ "+permission.Command, boxWidth)
	}
	if permission.Resolved {
		answer := "Answered"
		if permission.Result.OptionID != "" {
			answer = permission.Result.OptionID
			for _, option := range permission.Options {
				if option.ID == permission.Result.OptionID && option.Label != "" {
					answer = option.Label
					break
				}
			}
		}
		appendChatCardText(lines, "Answer: "+answer, boxWidth)
	} else if !permission.Inactive && len(permission.Options) > 0 {
		options := make([]string, 0, len(permission.Options))
		for _, option := range permission.Options {
			if option.Label != "" {
				options = append(options, option.Label)
			}
		}
		if len(options) > 0 {
			appendChatCardText(lines, "Options: "+strings.Join(options, " · "), boxWidth)
		}
	}
	appendChatWrapped(lines, "┗"+strings.Repeat("━", max(0, boxWidth-2))+"┛", width)
}

func appendChatElicitation(lines *[]string, data json.RawMessage, width int) {
	var ask struct {
		Prompt   string   `json:"prompt"`
		Choices  []string `json:"choices"`
		Resolved bool     `json:"resolved"`
		Inactive bool     `json:"inactive"`
		Result   struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(data, &ask)
	prompt := ask.Prompt
	if prompt == "" {
		prompt = "Input needed"
	}
	if ask.Resolved {
		appendChatWrapped(lines, "✓ "+prompt+" · Answer: "+ask.Result.Value, width)
		return
	}
	if ask.Inactive {
		appendChatWrapped(lines, "· "+prompt+" · No longer active", width)
		return
	}
	appendChatWrapped(lines, "? "+prompt, width)
	if len(ask.Choices) > 0 {
		appendChatWrapped(lines, "  Options: "+strings.Join(ask.Choices, " · "), width)
	}
}

type chatImageRef struct {
	SHA  string
	MIME string
}

func appendChatImageLines(lines *[]string, data json.RawMessage, width int, imageLines map[int]chatImageRef) {
	start := len(*lines)
	ref := appendChatImage(lines, data, width)
	if ref.SHA != "" && imageLines != nil {
		for line := start; line < len(*lines); line++ {
			imageLines[line] = ref
		}
	}
}

func appendChatImage(lines *[]string, data json.RawMessage, width int) chatImageRef {
	var body struct {
		Image *struct {
			SHA    string `json:"sha"`
			Blob   string `json:"blob"`
			MIME   string `json:"mime"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"image"`
		ResourceLink *struct {
			SHA  string `json:"sha"`
			Blob string `json:"blob"`
			Name string `json:"name"`
			MIME string `json:"mime"`
		} `json:"resource_link"`
	}
	_ = json.Unmarshal(data, &body)
	if body.Image == nil {
		if body.ResourceLink != nil {
			name := cleanChatText(body.ResourceLink.Name)
			if name == "" {
				name = "file"
			}
			appendChatWrapped(lines, "┃  ▣ File attached · "+name, width)
			return chatImageRef{SHA: chatImageSHA(body.ResourceLink.SHA, body.ResourceLink.Blob), MIME: body.ResourceLink.MIME}
		}
		return chatImageRef{}
	}
	label := "┃  ▣ Image attached"
	if body.Image.MIME != "" {
		label += " · " + body.Image.MIME
	}
	if body.Image.Width > 0 && body.Image.Height > 0 {
		label += fmt.Sprintf(" · %d×%d", body.Image.Width, body.Image.Height)
	}
	appendChatWrapped(lines, label, width)
	return chatImageRef{SHA: chatImageSHA(body.Image.SHA, body.Image.Blob), MIME: body.Image.MIME}
}

func chatImageSHA(sha, blob string) string {
	if sha != "" {
		return sha
	}
	return strings.TrimPrefix(blob, "sha256:")
}

func appendChatCardText(lines *[]string, text string, boxWidth int) {
	text = cleanChatText(text)
	contentWidth := max(1, boxWidth-4)
	for _, paragraph := range strings.Split(text, "\n") {
		wrapped := ansi.WrapWc(paragraph, contentWidth, " ")
		for _, line := range strings.Split(wrapped, "\n") {
			padding := max(0, contentWidth-ansi.StringWidth(line))
			*lines = append(*lines, "┃ "+line+strings.Repeat(" ", padding)+" ┃")
		}
	}
}

func appendChatWrapped(lines *[]string, text string, width int) {
	appendChatBlock(lines, "", text, width)
}

func appendChatBlock(lines *[]string, prefix, text string, width int) {
	text = cleanChatText(text)
	contentWidth := max(1, width-ansi.StringWidth(prefix))
	for _, paragraph := range strings.Split(text, "\n") {
		wrapped := ansi.WrapWc(paragraph, contentWidth, " ")
		for _, line := range strings.Split(wrapped, "\n") {
			*lines = append(*lines, prefix+line)
		}
	}
}

func cleanChatText(text string) string {
	return acp.StripControls(strings.ReplaceAll(text, "\t", "  "))
}
