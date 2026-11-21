package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	sdkacp "github.com/coder/acp-go-sdk"
)

func (c *HarnessClient) UnstableCreateElicitation(ctx context.Context, params sdkacp.UnstableCreateElicitationRequest) (sdkacp.UnstableCreateElicitationResponse, error) {
	if params.Url != nil {
		agentID := string(params.Url.ElicitationId)
		if agentID == "" {
			return sdkacp.UnstableCreateElicitationResponse{}, fmt.Errorf("elicitation ID is required")
		}
		prompt := params.Url.Message
		if prompt == "" {
			prompt = params.Url.Url
		}
		// The person opens the URL themselves; their answer only says whether they did.
		value, err := c.waitElicitation(ctx, map[string]any{"agentElicitationId": agentID, "prompt": prompt, "url": params.Url.Url})
		switch {
		case err != nil:
			return sdkacp.NewUnstableCreateElicitationResponseCancel(), nil
		case value == "decline":
			return sdkacp.NewUnstableCreateElicitationResponseDecline(), nil
		}
		return sdkacp.UnstableCreateElicitationResponse{Accept: &sdkacp.UnstableCreateElicitationAccept{Action: "accept"}}, nil
	}
	if params.Form == nil {
		return sdkacp.UnstableCreateElicitationResponse{}, fmt.Errorf("unsupported elicitation request")
	}
	fields := elicitationFields(params.Form.RequestedSchema)
	payload := map[string]any{"prompt": params.Form.Message}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	value, err := c.waitElicitation(ctx, payload)
	if err != nil {
		return sdkacp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	return sdkacp.UnstableCreateElicitationResponse{
		Accept: &sdkacp.UnstableCreateElicitationAccept{Action: "accept", Content: elicitationContent(fields, value)},
	}, nil
}

func (c *HarnessClient) UnstableCompleteElicitation(_ context.Context, _ sdkacp.UnstableCompleteElicitationNotification) error {
	return nil
}

type elicitationOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// elicitationField is one form property as the UI renders it: options to pick, or free text.
type elicitationField struct {
	Key         string              `json:"key"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Multiple    bool                `json:"multiple,omitempty"`
	Options     []elicitationOption `json:"options,omitempty"`
}

// elicitationFields reads the form schema in key order; Claude's AskUserQuestion names its
// fields question_<n> and question_<n>_custom, so that order keeps each question beside its note.
func elicitationFields(schema sdkacp.UnstableElicitationSchema) []elicitationField {
	fields := make([]elicitationField, 0, len(schema.Properties))
	for _, key := range slices.Sorted(maps.Keys(schema.Properties)) {
		prop, _ := schema.Properties[key].(map[string]any)
		field := elicitationField{Key: key}
		field.Title, _ = prop["title"].(string)
		field.Description, _ = prop["description"].(string)
		source := prop
		if prop["type"] == "array" {
			field.Multiple = true
			source, _ = prop["items"].(map[string]any)
		}
		field.Options = elicitationOptions(source)
		fields = append(fields, field)
	}
	return fields
}

// elicitationOptions accepts a plain enum or titled oneOf/anyOf options.
func elicitationOptions(prop map[string]any) []elicitationOption {
	var options []elicitationOption
	if enum, ok := prop["enum"].([]any); ok {
		for _, item := range enum {
			options = append(options, elicitationOption{Value: fmt.Sprint(item), Label: fmt.Sprint(item)})
		}
		return options
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		list, _ := prop[key].([]any)
		for _, item := range list {
			option, _ := item.(map[string]any)
			value, ok := option["const"]
			if !ok {
				continue
			}
			label, _ := option["title"].(string)
			if label == "" {
				label = fmt.Sprint(value)
			}
			description, _ := option["description"].(string)
			options = append(options, elicitationOption{Value: fmt.Sprint(value), Label: label, Description: description})
		}
	}
	return options
}

// elicitationContent maps the UI's answer to form content: a JSON object keyed by field, or one
// plain value for a single-field form.
func elicitationContent(fields []elicitationField, value string) map[string]any {
	var content map[string]any
	if json.Unmarshal([]byte(value), &content) == nil && content != nil {
		return content
	}
	if len(fields) == 1 {
		return map[string]any{fields[0].Key: value}
	}
	return map[string]any{"value": value}
}
