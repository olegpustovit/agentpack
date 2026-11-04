package hub

import (
	"encoding/json"

	"github.com/olegpustovit/agentpack/internal/acp"
)

type pendingPermission struct {
	requestID string
	optionIDs []string
}

func pendingPermissionFrom(state acp.ThreadState) pendingPermission {
	if !state.NeedsYou {
		return pendingPermission{}
	}
	for i := len(state.Items) - 1; i >= 0; i-- {
		if state.Items[i].Kind != "permission" {
			continue
		}
		var perm struct {
			RequestID string `json:"requestId"`
			Resolved  bool   `json:"resolved"`
			Inactive  bool   `json:"inactive"`
			Options   []struct {
				ID string `json:"id"`
			} `json:"options"`
		}
		if err := json.Unmarshal(state.Items[i].Data, &perm); err != nil || perm.RequestID == "" || perm.Resolved || perm.Inactive {
			continue
		}
		ids := make([]string, 0, len(perm.Options))
		for _, option := range perm.Options {
			if option.ID != "" {
				ids = append(ids, option.ID)
			}
		}
		return pendingPermission{requestID: perm.RequestID, optionIDs: ids}
	}
	return pendingPermission{}
}

func permissionOptionID(options []string, index int) string {
	if index >= 0 && index < len(options) {
		return options[index]
	}
	defaults := []string{"allow", "allow_always", "reject"}
	if index >= 0 && index < len(defaults) {
		return defaults[index]
	}
	return "allow"
}
