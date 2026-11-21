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
	if string(request.Params.ClientCapabilities["subagents"]) != `{}` {
		t.Fatalf("subagents capability = %s", request.Params.ClientCapabilities["subagents"])
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

func TestElicitationFieldsReadAskUserQuestionForm(t *testing.T) {
	// The shape claude-agent-acp sends for AskUserQuestion with two questions.
	schema := sdkacp.UnstableElicitationSchema{
		Type: sdkacp.UnstableElicitationSchemaTypeObject,
		Properties: map[string]any{
			"question_1":        map[string]any{"type": "array", "title": "Login", "items": map[string]any{"anyOf": []any{map[string]any{"const": "a", "title": "A"}}}},
			"question_0":        map[string]any{"type": "string", "title": "Commit", "oneOf": []any{map[string]any{"const": "yes", "title": "Commit everything", "description": "One commit"}}},
			"question_0_custom": map[string]any{"type": "string", "title": "Other"},
			"tone":              map[string]any{"type": "string", "enum": []any{"Direct"}},
		},
	}
	got := elicitationFields(schema)
	if len(got) != 4 || got[0].Key != "question_0" || got[1].Key != "question_0_custom" || got[2].Key != "question_1" {
		t.Fatalf("fields = %+v", got)
	}
	if o := got[0].Options; len(o) != 1 || o[0] != (elicitationOption{Value: "yes", Label: "Commit everything", Description: "One commit"}) {
		t.Fatalf("oneOf options = %+v", o)
	}
	if len(got[1].Options) != 0 || !got[2].Multiple || got[2].Options[0].Label != "A" || got[3].Options[0].Value != "Direct" {
		t.Fatalf("fields = %+v", got)
	}
	if c := elicitationContent(got, `{"question_0":"yes","question_1":["a"]}`); c["question_0"] != "yes" {
		t.Fatalf("object answer = %v", c)
	}
	if c := elicitationContent(got[3:], "Direct"); c["tone"] != "Direct" {
		t.Fatalf("plain answer = %v", c)
	}
}

func TestWithdrawnElicitationStopsNeedingTheUser(t *testing.T) {
	client := NewHarnessClient("", nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := sdkacp.NewUnstableCreateElicitationRequestForm(sdkacp.UnstableElicitationSchema{Type: sdkacp.UnstableElicitationSchemaTypeObject})
	response, err := client.UnstableCreateElicitation(ctx, request)
	if err != nil || response.Cancel == nil {
		t.Fatalf("response = %+v, %v", response, err)
	}
	entries, _ := client.Log.ReadSince(0)
	if state := ReduceLog(entries); state.NeedsYou {
		t.Fatalf("a withdrawn question still needs the user: %+v", entries)
	}
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

func TestBypassModeAnswersPermissionWithoutAskingOrLogging(t *testing.T) {
	log := NewEventLog(0)
	client := NewHarnessClient("", nil, log, nil)
	bypass := true
	client.AutoApprove = func() bool { return bypass }
	request := sdkacp.RequestPermissionRequest{
		ToolCall: sdkacp.ToolCallUpdate{ToolCallId: "tool-1"},
		Options: []sdkacp.PermissionOption{
			{OptionId: "always", Kind: sdkacp.PermissionOptionKindAllowAlways},
			{OptionId: "reject", Kind: sdkacp.PermissionOptionKindRejectOnce},
			{OptionId: "once", Kind: sdkacp.PermissionOptionKindAllowOnce},
		},
	}
	response, err := client.RequestPermission(context.Background(), request)
	if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "once" {
		t.Fatalf("bypass must allow once without a card: %+v %v", response, err)
	}
	if entries, _ := log.ReadSince(0); len(entries) != 0 {
		t.Fatalf("an automatic answer must not leave a card: %+v", entries)
	}
	// With no allow option it cannot answer for the user, so it still asks.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request.Options = request.Options[1:2]
	if _, err := client.RequestPermission(ctx, request); err == nil {
		t.Fatal("a request with no allow option must wait for the user")
	}
	// Leaving bypass mode asks again.
	bypass = false
	request.Options = []sdkacp.PermissionOption{{OptionId: "once", Kind: sdkacp.PermissionOptionKindAllowOnce}}
	if _, err := client.RequestPermission(ctx, request); err == nil {
		t.Fatal("outside bypass mode the user answers")
	}
}

// Codex asks to connect a ChatGPT app with a URL elicitation; the card shows the message and
// link, and the person's Done or Decline becomes the agent's accept or decline.
func TestURLElicitationShowsTheLinkAndAnswersAcceptOrDecline(t *testing.T) {
	for _, test := range []struct{ answer, want string }{{"done", "accept"}, {"decline", "decline"}} {
		client := NewHarnessClient("", nil, nil, nil)
		var shown map[string]string
		client.Log.SetOnAppend(func(entry LogEntry) {
			if entry.Kind != "elicitation" || json.Unmarshal(entry.Payload, &shown) != nil {
				return
			}
			if !client.Broker.Answer(shown["elicitationId"], test.answer) {
				t.Errorf("could not answer %s", shown["elicitationId"])
			}
		})
		response, err := client.UnstableCreateElicitation(context.Background(), sdkacp.UnstableCreateElicitationRequest{Url: &sdkacp.UnstableCreateElicitationUrl{
			ElicitationId: "codex_apps_auth_1", Message: "Connect Artifacts", Mode: "url", Url: "https://chatgpt.com/apps/artifacts/x",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if shown["prompt"] != "Connect Artifacts" || shown["url"] != "https://chatgpt.com/apps/artifacts/x" || shown["agentElicitationId"] != "codex_apps_auth_1" {
			t.Fatalf("shown = %v", shown)
		}
		raw, _ := json.Marshal(response)
		if string(raw) != `{"action":"`+test.want+`"}` {
			t.Fatalf("%s answered %s", test.answer, raw)
		}
	}
}
