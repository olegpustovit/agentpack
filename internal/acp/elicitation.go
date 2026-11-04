package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
)

func (c *HarnessClient) UnstableCreateElicitation(ctx context.Context, params sdkacp.UnstableCreateElicitationRequest) (sdkacp.UnstableCreateElicitationResponse, error) {
	if params.Url != nil {
		agentID := string(params.Url.ElicitationId)
		if agentID == "" {
			return sdkacp.UnstableCreateElicitationResponse{}, fmt.Errorf("elicitation ID is required")
		}
		id := c.nextRequestID("elicitation")
		if err := c.Broker.Begin(id); err != nil {
			return sdkacp.UnstableCreateElicitationResponse{}, err
		}
		defer c.Broker.End(id)
		c.Log.Append("elicitation", time.Now().UnixMilli(), mustJSON(map[string]any{
			"elicitationId":      id,
			"agentElicitationId": agentID,
			"prompt":             params.Url.Url,
		}))
		value, err := c.Broker.Wait(ctx, id)
		if err != nil {
			return sdkacp.NewUnstableCreateElicitationResponseCancel(), nil
		}
		return sdkacp.UnstableCreateElicitationResponse{
			Accept: &sdkacp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{"value": value}},
		}, nil
	}
	if params.Form == nil {
		return sdkacp.UnstableCreateElicitationResponse{}, fmt.Errorf("unsupported elicitation request")
	}
	id := c.nextRequestID("elicitation")
	if err := c.Broker.Begin(id); err != nil {
		return sdkacp.UnstableCreateElicitationResponse{}, err
	}
	defer c.Broker.End(id)
	choices := elicitationChoices(params.Form.RequestedSchema)
	payload := map[string]any{
		"elicitationId": id,
		"prompt":        params.Form.Message,
	}
	if len(choices) > 0 {
		payload["choices"] = choices
	}
	c.Log.Append("elicitation", time.Now().UnixMilli(), mustJSON(payload))
	value, err := c.Broker.Wait(ctx, id)
	if err != nil {
		return sdkacp.NewUnstableCreateElicitationResponseCancel(), nil
	}
	return sdkacp.UnstableCreateElicitationResponse{
		Accept: &sdkacp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{"value": value}},
	}, nil
}

func (c *HarnessClient) UnstableCompleteElicitation(_ context.Context, _ sdkacp.UnstableCompleteElicitationNotification) error {
	return nil
}

func elicitationChoices(schema sdkacp.UnstableElicitationSchema) []string {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	props, _ := doc["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	for _, prop := range props {
		m, _ := prop.(map[string]any)
		if enum, ok := m["enum"].([]any); ok {
			out := make([]string, 0, len(enum))
			for _, item := range enum {
				out = append(out, fmt.Sprint(item))
			}
			return out
		}
	}
	return nil
}
