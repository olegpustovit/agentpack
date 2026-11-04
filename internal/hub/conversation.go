package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/olegpustovit/agentpack/internal/network"
)

// shortcut: mirror the worker's 20 MiB blob cap; update this if that shared limit changes.
const maxHubImageBytes = 20 << 20
const maxHubImageCacheBytes = 100 << 20

func postConversation(ctx context.Context, home, sessionID, nodeID, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/conversation/" + endpoint
	_, err = network.NodeRequest(ctx, home, http.MethodPost, gatewayPeerPath(path, nodeID), bytes.NewReader(body))
	return err
}
func parseHubConversationConfig(data []byte) ([]hubConfigOption, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if raw, ok := fields["configOptions"]; ok {
		var options []hubConfigOption
		if err := json.Unmarshal(raw, &options); err != nil {
			return nil, err
		}
		return options, nil
	}
	var options []hubConfigOption
	var modes struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"availableModes"`
	}
	if json.Unmarshal(fields["modes"], &modes) == nil && len(modes.AvailableModes) > 0 {
		option := hubConfigOption{ID: "mode", Name: "Permissions", Category: "mode", CurrentValue: modes.CurrentModeID}
		for _, mode := range modes.AvailableModes {
			option.Options = append(option.Options, hubConfigChoice{Value: mode.ID, Name: mode.Name})
		}
		options = append(options, option)
	}
	var models struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"availableModels"`
	}
	if json.Unmarshal(fields["models"], &models) == nil && len(models.AvailableModels) > 0 {
		option := hubConfigOption{ID: "model", Name: "Model", Category: "model", CurrentValue: models.CurrentModelID}
		for _, model := range models.AvailableModels {
			option.Options = append(option.Options, hubConfigChoice{Value: model.ID, Name: model.Name})
		}
		options = append(options, option)
	}
	return options, nil
}

func conversationConfig(ctx context.Context, home, sessionID, machine string) ([]hubConfigOption, error) {
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/conversation/config-options"
	data, err := network.NodeRequest(ctx, home, http.MethodGet, gatewayPeerPath(path, machine), nil)
	if err != nil {
		return nil, err
	}
	return parseHubConversationConfig(data)
}

func configOptionFor(options []hubConfigOption, category string) *hubConfigOption {
	for i := range options {
		if options[i].Category == category {
			return &options[i]
		}
	}
	return nil
}

func configOptionName(option hubConfigOption) string {
	if option.Name != "" {
		return option.Name
	}
	switch option.Category {
	case "mode":
		return "Permissions"
	case "thought_level":
		return "Effort"
	default:
		return "Model"
	}
}

func configValueLabel(option hubConfigOption) string {
	for _, choice := range option.Options {
		if choice.Value == option.CurrentValue {
			if choice.Name != "" {
				return choice.Name
			}
			return choice.Value
		}
	}
	return option.CurrentValue
}

func (h *home) openConfigPicker(category string) tea.Cmd {
	if !h.previewConfigLoaded {
		h.previewActionErr = errors.New("agent settings are still loading")
		return nil
	}
	if h.previewConfigErr != nil {
		h.previewActionErr = fmt.Errorf("could not load agent settings: %w", h.previewConfigErr)
		return nil
	}
	option := configOptionFor(h.previewConfigOptions, category)
	if option == nil || len(option.Options) == 0 {
		h.previewActionErr = fmt.Errorf("%s options are not available for this session", configCategoryLabel(category))
		return nil
	}
	h.configCategory, h.configCursor = category, 0
	for i, choice := range option.Options {
		if choice.Value == option.CurrentValue {
			h.configCursor = i
			break
		}
	}
	h.configFrom, h.panel = h.panel, panelConfig
	return nil
}

func (h *home) setConfigChoiceCmd() tea.Cmd {
	option := configOptionFor(h.previewConfigOptions, h.configCategory)
	if option == nil || h.configCursor < 0 || h.configCursor >= len(option.Options) {
		return nil
	}
	session := h.selectedSession()
	if session == nil {
		return nil
	}
	home, sessionID, machine := h.agentpackHome, session.info.ID, session.machine
	category, key, value := h.configCategory, option.ID, option.Options[h.configCursor].Value
	optionName := configOptionName(*option)
	return func() tea.Msg {
		raw, err := json.Marshal(value)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = postConversation(ctx, home, sessionID, machine, "config", struct {
				Key   string          `json:"key"`
				Value json.RawMessage `json:"value"`
			}{Key: key, Value: raw})
		}
		if err != nil {
			return configSetMsg{sessionID: sessionID, machine: machine, category: category, key: key, value: value, err: fmt.Errorf("could not set %s: %w", optionName, err)}
		}
		return configSetMsg{sessionID: sessionID, machine: machine, category: category, key: key, value: value}
	}
}
func sessionCanFork(session *listedSession) bool {
	return session != nil && session.info.Transport == "acp" && session.info.AgentSessionID != "" &&
		session.info.AgentCapabilities != nil && session.info.AgentCapabilities.SessionCapabilities != nil &&
		session.info.AgentCapabilities.SessionCapabilities.Fork != nil
}

func (h *home) forkSelectedSessionCmd() tea.Cmd {
	session := h.selectedSession()
	if !sessionCanFork(session) {
		return func() tea.Msg {
			return forkedSessionMsg{err: errors.New("this agent session does not advertise fork support")}
		}
	}
	home, sessionID, machine := h.agentpackHome, session.info.ID, session.machine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		path := "/api/sessions/" + url.PathEscape(sessionID) + "/fork"
		data, err := network.NodeRequest(ctx, home, http.MethodPost, gatewayPeerPath(path, machine), nil)
		if err != nil {
			return forkedSessionMsg{machine: machine, err: fmt.Errorf("could not fork session: %w", err)}
		}
		var info network.SessionInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return forkedSessionMsg{machine: machine, err: fmt.Errorf("decode forked session: %w", err)}
		}
		if info.ID == "" {
			return forkedSessionMsg{machine: machine, err: errors.New("gateway returned an empty forked session ID")}
		}
		return forkedSessionMsg{info: info, machine: machine}
	}
}

func copyChatMessage(text string) tea.Cmd {
	return tea.SetClipboard(cleanChatText(text))
}

type conversationSentMsg struct {
	sessionID     string
	machine       string
	err           error
	clearComposer bool
	composeAgain  bool
}

func dropLastRune(text string) string {
	if text == "" {
		return text
	}
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}

func (h *home) sendPreviewImageCmd(path, text string) tea.Cmd {
	session := h.selectedSession()
	if session == nil {
		return func() tea.Msg { return conversationSentMsg{err: errors.New("no session selected"), composeAgain: true} }
	}
	if session.info.Transport != "acp" {
		return func() tea.Msg {
			return conversationSentMsg{sessionID: session.info.ID, machine: session.machine, err: errors.New("images require a chat session"), composeAgain: true}
		}
	}
	home, sessionID, nodeID, directory, harness := h.agentpackHome, session.info.ID, session.machine, session.info.Directory, session.info.Harness
	return func() tea.Msg {
		if strings.TrimSpace(path) == "" {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: errors.New("enter an image path"), composeAgain: true}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		file, err := os.Open(path)
		if err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("open image: %w", err), composeAgain: true}
		}
		info, statErr := file.Stat()
		if statErr != nil {
			file.Close()
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("inspect image: %w", statErr), composeAgain: true}
		}
		if !info.Mode().IsRegular() || info.Size() > maxHubImageBytes {
			file.Close()
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("image must be a regular file smaller than %d bytes", maxHubImageBytes), composeAgain: true}
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxHubImageBytes+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("read image: %w", err), composeAgain: true}
		}
		if len(data) == 0 || len(data) > maxHubImageBytes {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("image must contain 1 to %d bytes", maxHubImageBytes), composeAgain: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		blobPath := "/api/sessions/" + url.PathEscape(sessionID) + "/conversation/blobs"
		response, err := network.NodeRequest(ctx, home, http.MethodPost, gatewayPeerPath(blobPath, nodeID), bytes.NewReader(data))
		if err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("upload image: %w", err), composeAgain: true}
		}
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(response, &blob); err != nil || blob.SHA == "" {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: errors.New("gateway returned an invalid image reference"), composeAgain: true}
		}
		var payload any
		if harness == "agy" {
			payload = struct {
				Text         string `json:"text,omitempty"`
				ResourceLink struct {
					SHA  string `json:"sha"`
					Name string `json:"name"`
				} `json:"resource_link"`
			}{Text: text, ResourceLink: struct {
				SHA  string `json:"sha"`
				Name string `json:"name"`
			}{SHA: blob.SHA, Name: filepath.Base(path)}}
		} else {
			payload = struct {
				Text  string `json:"text,omitempty"`
				Image struct {
					SHA string `json:"sha"`
				} `json:"image"`
			}{Text: text, Image: struct {
				SHA string `json:"sha"`
			}{SHA: blob.SHA}}
		}
		if err := postConversation(ctx, home, sessionID, nodeID, "prompt", payload); err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("send image prompt: %w", err), composeAgain: true}
		}
		return conversationSentMsg{sessionID: sessionID, machine: nodeID, clearComposer: true}
	}
}

func (h *home) sendPreviewPromptCmd(text, imagePath string) tea.Cmd {
	if imagePath != "" {
		return h.sendPreviewImageCmd(imagePath, text)
	}
	session := h.selectedSession()
	if session == nil {
		return func() tea.Msg { return conversationSentMsg{err: errors.New("no session selected"), composeAgain: true} }
	}
	if session.info.Transport != "acp" || strings.TrimSpace(text) == "" {
		return func() tea.Msg {
			return conversationSentMsg{sessionID: session.info.ID, machine: session.machine, err: errors.New("enter a message for an ACP chat session"), composeAgain: true}
		}
	}
	home, sessionID, nodeID := h.agentpackHome, session.info.ID, session.machine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := postConversation(ctx, home, sessionID, nodeID, "prompt", struct {
			Text string `json:"text"`
		}{Text: strings.TrimSpace(text)}); err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("could not send message: %w", err), composeAgain: true}
		}
		return conversationSentMsg{sessionID: sessionID, machine: nodeID, clearComposer: true}
	}
}

type chatImageActionMsg struct {
	err error
}

func chatImageCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "agentpack", "chat-images")
	if err := network.EnsurePrivateDirectory(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func cacheChatImage(ctx context.Context, home, sessionID, nodeID string, ref chatImageRef, dir string) (string, error) {
	hashBytes, err := hex.DecodeString(ref.SHA)
	if err != nil || len(hashBytes) != sha256.Size {
		return "", errors.New("invalid image reference")
	}
	sha := hex.EncodeToString(hashBytes)
	if err := network.EnsurePrivateDirectory(dir); err != nil {
		return "", err
	}
	ext := ".img"
	if extensions, err := mime.ExtensionsByType(ref.MIME); err == nil && len(extensions) > 0 {
		ext = extensions[0]
	}
	path := filepath.Join(dir, sha+ext)
	if cachedChatImageMatches(path, sha) {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	if _, err := os.Lstat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".agentpack-image-*")
	if err != nil {
		return "", err
	}
	temp := file.Name()
	defer os.Remove(temp)
	digest := sha256.New()
	blobPath := "/api/sessions/" + url.PathEscape(sessionID) + "/blobs/" + url.PathEscape(sha)
	if err := network.NodeRequestStream(ctx, home, http.MethodGet, gatewayPeerPath(blobPath, nodeID), io.MultiWriter(file, digest), maxHubImageBytes); err != nil {
		file.Close()
		return "", fmt.Errorf("download image: %w", err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != sha {
		file.Close()
		return "", errors.New("downloaded image digest does not match its reference")
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temp, path); err != nil && !cachedChatImageMatches(path, sha) {
		return "", err
	}
	pruneChatImageCache(dir, path)
	return path, nil
}

func cachedChatImageMatches(path, sha string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxHubImageBytes {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false
	}
	return hex.EncodeToString(digest.Sum(nil)) == sha
}

func pruneChatImageCache(dir, keep string) {
	type cached struct {
		path    string
		size    int64
		modTime time.Time
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []cached
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || len(strings.TrimSuffix(name, filepath.Ext(name))) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, filepath.Ext(name))); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		files = append(files, cached{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= maxHubImageCacheBytes {
			break
		}
		if file.path == keep {
			continue
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.size
		}
	}
}

func openImageWithSystem(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", path)
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

func (h *home) openChatImageCmd(ref chatImageRef) tea.Cmd {
	session := h.selectedSession()
	if session == nil || session.info.Transport != "acp" || ref.SHA == "" {
		return nil
	}
	home, sessionID, nodeID := h.agentpackHome, session.info.ID, session.machine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		dir, err := chatImageCacheDir()
		if err == nil {
			var path string
			path, err = cacheChatImage(ctx, home, sessionID, nodeID, ref, dir)
			if err == nil {
				err = openImageWithSystem(path)
			}
		}
		return chatImageActionMsg{err: err}
	}
}

func (h *home) answerPermissionViaGateway(optionID string) tea.Cmd {
	session := h.selectedSession()
	if session == nil || h.previewPerm.requestID == "" || optionID == "" {
		return func() tea.Msg { return conversationSentMsg{err: errors.New("permission request is no longer pending")} }
	}
	home, sessionID, nodeID, requestID := h.agentpackHome, session.info.ID, session.machine, h.previewPerm.requestID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := postConversation(ctx, home, sessionID, nodeID, "permission", struct {
			RequestID string `json:"request_id"`
			OptionID  string `json:"option_id"`
		}{RequestID: requestID, OptionID: optionID}); err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("could not answer permission: %w", err)}
		}
		return conversationSentMsg{sessionID: sessionID, machine: nodeID}
	}
}

func (h *home) answerElicitationViaGateway(value string) tea.Cmd {
	session := h.selectedSession()
	if session == nil || h.previewElicit.id == "" || value == "" {
		return func() tea.Msg { return conversationSentMsg{err: errors.New("question is no longer pending")} }
	}
	home, sessionID, nodeID, id := h.agentpackHome, session.info.ID, session.machine, h.previewElicit.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := postConversation(ctx, home, sessionID, nodeID, "elicitation", struct {
			ID    string `json:"elicitation_id"`
			Value string `json:"value"`
		}{ID: id, Value: value}); err != nil {
			return conversationSentMsg{sessionID: sessionID, machine: nodeID, err: fmt.Errorf("could not answer question: %w", err)}
		}
		return conversationSentMsg{sessionID: sessionID, machine: nodeID}
	}
}
