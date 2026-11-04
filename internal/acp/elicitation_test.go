package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkacp "github.com/coder/acp-go-sdk"
)

func TestInitializeRequestAdvertisesElicitation(t *testing.T) {
	req := InitializeRequest()
	if req.ClientCapabilities.Elicitation == nil || req.ClientCapabilities.Elicitation.Form == nil {
		t.Fatal("expected form elicitation capability")
	}
	if !req.ClientCapabilities.Fs.ReadTextFile || !req.ClientCapabilities.Fs.WriteTextFile {
		t.Fatal("expected fs capabilities")
	}
}
func TestInitializeRequestAdvertisesStandardSessionCapabilitiesJSON(t *testing.T) {
	params, err := json.Marshal(InitializeRequest())
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{Method: "initialize", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	line = AddSessionCapabilities(line)
	var request struct {
		Params struct {
			ClientCapabilities map[string]json.RawMessage `json:"clientCapabilities"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatal(err)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(request.Params.ClientCapabilities["session"], &session); err != nil {
		t.Fatal(err)
	}
	if string(session["configOptions"]) != `{"boolean":{}}` || string(session["notices"]) != `{}` || string(session["compaction"]) != `{}` {
		t.Fatalf("session capability JSON = %s", request.Params.ClientCapabilities["session"])
	}
	if strings.Contains(string(request.Params.ClientCapabilities["_meta"]), `"session"`) {
		t.Fatal("standard session capabilities must not be advertised under _meta")
	}
}
func TestSessionUpdateForwardsRawACPUsage(t *testing.T) {
	for _, test := range []struct {
		name string
		cost *sdkacp.Cost
		want string
	}{
		{name: "with cost", cost: &sdkacp.Cost{Amount: 0.42, Currency: "USD"}, want: `{"cost":{"amount":0.42,"currency":"USD"},"sessionUpdate":"usage_update","size":40,"used":12}`},
		{name: "without cost", want: `{"sessionUpdate":"usage_update","size":40,"used":12}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := NewEventLog(0)
			client := NewHarnessClient("", nil, log, nil)
			if err := client.SessionUpdate(context.Background(), sdkacp.SessionNotification{
				Update: sdkacp.SessionUpdate{UsageUpdate: &sdkacp.SessionUsageUpdate{
					SessionUpdate: "usage_update",
					Size:          40,
					Used:          12,
					Cost:          test.cost,
				}},
			}); err != nil {
				t.Fatal(err)
			}
			entries := log.Snapshot()
			if len(entries) != 1 || entries[0].Kind != "usage_update" || string(entries[0].Payload) != test.want {
				t.Fatalf("raw usage log = %+v, want %s", entries, test.want)
			}
		})
	}
}
func TestSessionUpdateForwardsRemainingSDKVariants(t *testing.T) {
	title := "Session title"
	tests := []struct {
		kind, field string
		update      sdkacp.SessionUpdate
	}{
		{
			kind: "session_info_update", field: "title",
			update: sdkacp.SessionUpdate{SessionInfoUpdate: &sdkacp.SessionSessionInfoUpdate{
				SessionUpdate: "session_info_update", Title: &title,
			}},
		},
		{
			kind: "available_commands_update", field: "availableCommands",
			update: sdkacp.SessionUpdate{AvailableCommandsUpdate: &sdkacp.SessionAvailableCommandsUpdate{
				SessionUpdate: "available_commands_update", AvailableCommands: []sdkacp.AvailableCommand{},
			}},
		},
		{
			kind: "current_mode_update", field: "currentModeId",
			update: sdkacp.SessionUpdate{CurrentModeUpdate: &sdkacp.SessionCurrentModeUpdate{
				SessionUpdate: "current_mode_update", CurrentModeId: sdkacp.SessionModeId("ask"),
			}},
		},
		{
			kind: "config_option_update", field: "configOptions",
			update: sdkacp.SessionUpdate{ConfigOptionUpdate: &sdkacp.SessionConfigOptionUpdate{
				SessionUpdate: "config_option_update", ConfigOptions: []sdkacp.SessionConfigOption{},
			}},
		},
		{
			kind: "plan_update", field: "plan",
			update: sdkacp.SessionUpdate{PlanUpdate: &sdkacp.SessionPlanUpdate{
				SessionUpdate: "plan_update",
				Plan: sdkacp.PlanUpdateContent{Markdown: &sdkacp.PlanUpdateContentMarkdown{
					Id: sdkacp.PlanId("p1"), Type: "markdown", Content: "Plan text",
				}},
			}},
		},
		{
			kind: "plan_removed", field: "id",
			update: sdkacp.SessionUpdate{PlanRemoved: &sdkacp.SessionUpdatePlanRemoved{
				SessionUpdate: "plan_removed", Id: sdkacp.PlanId("p1"),
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			log := NewEventLog(0)
			client := NewHarnessClient("", nil, log, nil)
			if err := client.SessionUpdate(context.Background(), sdkacp.SessionNotification{Update: test.update}); err != nil {
				t.Fatal(err)
			}
			entries := log.Snapshot()
			var payload map[string]json.RawMessage
			if len(entries) != 1 || entries[0].Kind != test.kind || json.Unmarshal(entries[0].Payload, &payload) != nil {
				t.Fatalf("event = %+v", entries)
			}
			if string(payload["sessionUpdate"]) != `"`+test.kind+`"` || len(payload[test.field]) == 0 {
				t.Fatalf("event %q payload = %s", test.kind, entries[0].Payload)
			}
		})
	}
}

func TestCursorAskQuestionWaitsForSequentialAnswers(t *testing.T) {
	client := NewHarnessClient("", nil, nil, nil)
	var questions []string
	client.Log.SetOnAppend(func(entry LogEntry) {
		if entry.Kind != "elicitation" {
			return
		}
		var payload struct {
			ElicitationID string `json:"elicitationId"`
			QuestionID    string `json:"questionId"`
			AllowMultiple bool   `json:"allowMultiple"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Error(err)
			return
		}
		questions = append(questions, payload.QuestionID)
		answer := "yes"
		if payload.AllowMultiple {
			answer = `["b","g"]`
		}
		if !client.Broker.Answer(payload.ElicitationID, answer) {
			t.Errorf("could not answer %s", payload.QuestionID)
		}
	})
	result, err := client.HandleExtensionMethod(context.Background(), "_agentpack/cursor/ask_question", json.RawMessage(`{
		"toolCallId":"tool-1",
		"title":"Choose",
		"questions":[
			{"id":"colors","prompt":"Pick colors","allowMultiple":true,"options":[{"id":"b","label":"Blue"},{"id":"g","label":"Green"}]},
			{"id":"confirm","prompt":"Continue?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Outcome struct {
			Outcome string `json:"outcome"`
			Answers []struct {
				QuestionID        string   `json:"questionId"`
				SelectedOptionIDs []string `json:"selectedOptionIds"`
			} `json:"answers"`
		} `json:"outcome"`
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome.Outcome != "answered" || len(response.Outcome.Answers) != 2 ||
		response.Outcome.Answers[0].QuestionID != "colors" ||
		strings.Join(response.Outcome.Answers[0].SelectedOptionIDs, ",") != "b,g" ||
		response.Outcome.Answers[1].QuestionID != "confirm" ||
		strings.Join(response.Outcome.Answers[1].SelectedOptionIDs, ",") != "yes" {
		t.Fatalf("Cursor ask_question result = %+v", response.Outcome)
	}
	if strings.Join(questions, ",") != "colors,confirm" {
		t.Fatalf("elicitation sequence = %v", questions)
	}
}

func TestCursorCreatePlanNeedsExplicitDecision(t *testing.T) {
	params := json.RawMessage(`{"toolCallId":"tool-2","name":"Refactor","plan":"Inspect and update.","todos":[{"id":"t1","content":"Inspect","status":"pending"}]}`)
	client := NewHarnessClient("", nil, nil, nil)
	client.Log.SetOnAppend(func(entry LogEntry) {
		var payload struct {
			ElicitationID string `json:"elicitationId"`
			Plan          string `json:"plan"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Error(err)
			return
		}
		if payload.Plan != "Inspect and update." || !client.Broker.Answer(payload.ElicitationID, "Reject plan") {
			t.Error("plan card did not preserve the plan or accept an explicit rejection")
		}
	})
	result, err := client.HandleExtensionMethod(context.Background(), "_agentpack/cursor/create_plan", params)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Outcome struct {
			Outcome string `json:"outcome"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome.Outcome != "rejected" {
		t.Fatalf("plan rejection outcome = %q", response.Outcome.Outcome)
	}

	cancelled := NewHarnessClient("", nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = cancelled.HandleExtensionMethod(ctx, "_agentpack/cursor/create_plan", params)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome.Outcome != "cancelled" {
		t.Fatalf("unanswered plan outcome = %q, want cancelled", response.Outcome.Outcome)
	}
}

func TestElicitationChoicesFromEnumSchema(t *testing.T) {
	schema := sdkacp.UnstableElicitationSchema{
		Type: sdkacp.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{
			"tone": map[string]any{"type": "string", "enum": []any{"Direct", "Friendly"}},
		},
	}
	got := elicitationChoices(schema)
	if len(got) != 2 || got[0] != "Direct" {
		t.Fatalf("got %v", got)
	}
	_, _ = json.Marshal(schema)
}

func TestRequestPermissionRejectsEmptyRequestID(t *testing.T) {
	client := NewHarnessClient("", nil, nil, nil)
	if _, err := client.RequestPermission(context.Background(), sdkacp.RequestPermissionRequest{}); err == nil {
		t.Fatal("empty permission request ID should be rejected")
	}
}

func TestPermissionRequestIDDoesNotReuseToolCallID(t *testing.T) {
	client := NewHarnessClient("", nil, nil, nil)
	toolCallID := sdkacp.ToolCallId("agent-tool-1")
	first, err := client.permissionRequestID(toolCallID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.permissionRequestID(toolCallID)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == string(toolCallID) || second == string(toolCallID) {
		t.Fatalf("request IDs were not unique: first=%q second=%q tool=%q", first, second, toolCallID)
	}
	var payload struct {
		RequestID string `json:"requestId"`
		ToolCall  string `json:"toolCallId"`
	}
	raw := permissionRequestPayload(first, sdkacp.RequestPermissionRequest{
		ToolCall: sdkacp.ToolCallUpdate{ToolCallId: toolCallID},
	})
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RequestID != first || payload.ToolCall != string(toolCallID) {
		t.Fatalf("permission payload = %+v", payload)
	}
	broker := NewBroker(nil, nil, nil, nil)
	if err := broker.Begin(first); err != nil {
		t.Fatal(err)
	}
	if !broker.Answer(first, "allow") {
		t.Fatal("first request answer failed")
	}
	broker.End(first)
	if err := broker.Begin(second); err != nil {
		t.Fatal(err)
	}
	if broker.Answer(first, "deny") {
		t.Fatal("old permission card answered the later request")
	}
	if !broker.Answer(second, "allow") {
		t.Fatal("second request answer failed")
	}
	if got, err := broker.Wait(context.Background(), second); err != nil || got != "allow" {
		t.Fatalf("second request result = %q, %v", got, err)
	}
	broker.End(second)
}
