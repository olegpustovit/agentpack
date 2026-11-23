package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/olegpustovit/agentpack/internal/session"
)

// ConversationPrompt sends a text prompt to the session worker.
func ConversationPrompt(ctx context.Context, home, sessionID, text string) error {
	record, err := ReadWorkerRecord(home, sessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+record.Address+"/conversation/prompt", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return session.ErrSessionUnavailable
	}
	return nil
}
