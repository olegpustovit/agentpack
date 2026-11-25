package acp

import (
	"bytes"
	"encoding/json"
	"slices"
)

const GoalControlMethod = "_session/goal"

// GoalControlSupport reads the AIR goal capability from an initialize response.
func GoalControlSupport(meta map[string]any) (reports, pause bool) {
	jetbrains, _ := meta["jetbrains"].(map[string]any)
	air, _ := jetbrains["air"].(map[string]any)
	goal, _ := air["goal"].(map[string]any)
	if goal["version"] != float64(1) || goal["controlMethod"] != GoalControlMethod {
		return false, false
	}
	actions, _ := goal["actions"].([]any)
	return true, slices.Contains(actions, "pause")
}

// AIRGoal extracts the goal from a session_info_update. A missing key leaves the goal
// alone, while JSON null clears it.
func AIRGoal(payload json.RawMessage) json.RawMessage {
	if !bytes.Contains(payload, []byte(`"goal"`)) {
		return nil
	}
	var update struct {
		Meta struct {
			JetBrains struct {
				Air struct {
					Goal json.RawMessage `json:"goal"`
				} `json:"air"`
			} `json:"jetbrains"`
		} `json:"_meta"`
	}
	if json.Unmarshal(payload, &update) != nil {
		return nil
	}
	return update.Meta.JetBrains.Air.Goal
}
