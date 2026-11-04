package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

// ConversationLog reads the worker conversation NDJSON log for a local session.
func ConversationLog(ctx context.Context, home, sessionID string, since int64) ([]acp.LogEntry, error) {
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return nil, err
	}
	path := "/conversation?since=" + strconv.FormatInt(since, 10)
	response, err := workerRequest(ctx, record, "GET", path, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errSessionUnavailable
	}
	return readConversationNDJSON(response.Body)
}

// ConversationAnswerPermission forwards a permission answer to the session worker.
func ConversationAnswerPermission(ctx context.Context, home, sessionID, requestID, optionID string) error {
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"request_id": requestID, "option_id": optionID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+record.Address+"/conversation/permission", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errSessionUnavailable
	}
	return nil
}

// ConversationPrompt sends a text prompt to the session worker.
func ConversationPrompt(ctx context.Context, home, sessionID, text string) error {
	record, err := workerRecord(home, sessionID)
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
		return errSessionUnavailable
	}
	return nil
}

// ConversationPromptImages sends a prompt with stored image attachments to the session worker.
func ConversationPromptImages(ctx context.Context, home, sessionID, text string, images []acp.PromptImage) error {
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return err
	}
	type promptImage struct {
		Sha    string `json:"sha"`
		Name   string `json:"name"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	wireImages := make([]promptImage, len(images))
	for i, image := range images {
		wireImages[i] = promptImage{Sha: image.Sha, Name: image.Name, Width: image.Width, Height: image.Height}
	}
	body, err := json.Marshal(struct {
		Text   string        `json:"text"`
		Images []promptImage `json:"images"`
	}{Text: text, Images: wireImages})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+record.Address+"/conversation/prompt/images", bytes.NewReader(body))
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
		return errSessionUnavailable
	}
	return nil
}

// ConversationAnswerElicitation forwards an elicitation answer to the session worker.
func ConversationAnswerElicitation(ctx context.Context, home, sessionID, elicitationID, value string) error {
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"elicitation_id": elicitationID, "value": value})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+record.Address+"/conversation/elicitation", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errSessionUnavailable
	}
	return nil
}

func readConversationNDJSON(r io.Reader) ([]acp.LogEntry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var entries []acp.LogEntry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry acp.LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}
