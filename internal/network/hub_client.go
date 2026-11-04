package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/tmaxmax/go-sse"
)

const maxConversationSnapshotBytes = 4 << 20
const conversationUpdateBuffer = 16

var gatewayHTTPClient = http.Client{
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type ConversationUpdate struct {
	Entry     acp.LogEntry
	Truncated bool
	Err       error
}

func gatewayAPIRequest(ctx context.Context, home, method, path string, body io.Reader) (*http.Response, error) {
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("gateway: invalid API path")
	}
	record, err := loadNode(home)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+record.Address+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	return gatewayHTTPClient.Do(request)
}
func conversationRequest(ctx context.Context, home, sessionID, nodeID, method string, query url.Values) (*http.Response, error) {
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/conversation?" + query.Encode()
	local := nodeID == "" || nodeID == "local"
	if local {
		enabled, err := GatewayEnabled(home)
		if err != nil || !enabled {
			return workerConversationRequest(ctx, home, sessionID, method, "/conversation?"+query.Encode())
		}
	}
	response, err := gatewayAPIRequest(ctx, home, method, path, nil)
	if err != nil && local {
		return workerConversationRequest(ctx, home, sessionID, method, "/conversation?"+query.Encode())
	}
	return response, err
}

func workerConversationRequest(ctx context.Context, home, sessionID, method, path string) (*http.Response, error) {
	record, err := workerRecord(home, sessionID)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+record.Address+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	return gatewayHTTPClient.Do(request)
}

func ConversationSnapshot(ctx context.Context, home, sessionID, nodeID string, since int64) ([]acp.LogEntry, bool, error) {
	query := url.Values{"since": {strconv.FormatInt(since, 10)}}
	if nodeID != "" && nodeID != "local" {
		query.Set("node", nodeID)
	}
	response, err := conversationRequest(ctx, home, sessionID, nodeID, http.MethodGet, query)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, false, fmt.Errorf("gateway conversation: %s", strings.TrimSpace(string(message)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxConversationSnapshotBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxConversationSnapshotBytes {
		return nil, false, errors.New("gateway conversation snapshot exceeds 4 MiB")
	}
	entries, err := readConversationNDJSON(bytes.NewReader(data))
	return entries, response.Header.Get("X-Agentpack-Truncated") == "true", err
}

func SubscribeConversation(ctx context.Context, home, sessionID, nodeID string, since int64) (<-chan ConversationUpdate, context.CancelFunc, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	query := url.Values{"since": {strconv.FormatInt(since, 10)}, "stream": {"1"}}
	if nodeID != "" && nodeID != "local" {
		query.Set("node", nodeID)
	}
	response, err := conversationRequest(streamCtx, home, sessionID, nodeID, http.MethodGet, query)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		response.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("gateway conversation stream: %s", strings.TrimSpace(string(message)))
	}
	updates := make(chan ConversationUpdate, conversationUpdateBuffer)
	go func() {
		defer close(updates)
		defer response.Body.Close()
		for event, err := range sse.Read(response.Body, nil) {
			update := ConversationUpdate{}
			if err != nil {
				if streamCtx.Err() != nil {
					return
				}
				update.Err = err
			} else if event.Type == "truncated" {
				update.Truncated = true
			} else if event.Data == "" {
				continue
			} else if err := json.Unmarshal([]byte(event.Data), &update.Entry); err != nil {
				update.Err = err
			}
			select {
			case updates <- update:
			case <-streamCtx.Done():
				return
			}
			if update.Err != nil {
				return
			}
		}
	}()
	return updates, cancel, nil
}
