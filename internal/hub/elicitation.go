package hub

import (
	"encoding/json"

	"github.com/olegpustovit/agentpack/internal/acp"
)

type pendingElicitation struct {
	id            string
	choices       []string
	optionIDs     []string
	allowMultiple bool
}

func pendingElicitationFrom(state acp.ThreadState) pendingElicitation {
	if !state.NeedsYou {
		return pendingElicitation{}
	}
	for i := len(state.Items) - 1; i >= 0; i-- {
		if state.Items[i].Kind != "elicitation" {
			continue
		}
		var ask struct {
			ElicitationID string   `json:"elicitationId"`
			Choices       []string `json:"choices"`
			OptionIDs     []string `json:"optionIds"`
			AllowMultiple bool     `json:"allowMultiple"`
			Resolved      bool     `json:"resolved"`
			Inactive      bool     `json:"inactive"`
		}
		if err := json.Unmarshal(state.Items[i].Data, &ask); err != nil || ask.ElicitationID == "" || ask.Resolved || ask.Inactive {
			continue
		}
		return pendingElicitation{id: ask.ElicitationID, choices: ask.Choices, optionIDs: ask.OptionIDs, allowMultiple: ask.AllowMultiple}
	}
	return pendingElicitation{}
}

func elicitationChoice(pending pendingElicitation, index int) string {
	if index < 0 || index >= len(pending.choices) {
		return ""
	}
	if index < len(pending.optionIDs) && pending.optionIDs[index] != "" {
		return pending.optionIDs[index]
	}
	return pending.choices[index]
}

func toggleElicitationSelection(selected []string, value string) []string {
	for i, item := range selected {
		if item == value {
			return append(selected[:i], selected[i+1:]...)
		}
	}
	return append(selected, value)
}

func previewPending(state acp.ThreadState) (pendingPermission, pendingElicitation) {
	perm := pendingPermissionFrom(state)
	if perm.requestID != "" {
		return perm, pendingElicitation{}
	}
	return perm, pendingElicitationFrom(state)
}
