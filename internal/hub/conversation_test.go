package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/olegpustovit/agentpack/internal/network"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func conversationTestHome(t *testing.T, handler http.Handler) *home {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	const id = "session-aaa"
	const secret = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"
	homeDir := t.TempDir()
	networkDir := filepath.Join(homeDir, "network")
	sessionDir := filepath.Join(networkDir, "sessions", id)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	address := strings.TrimPrefix(server.URL, "http://")
	for path, value := range map[string]any{
		filepath.Join(networkDir, "node.json"):   map[string]string{"address": address, "secret": secret},
		filepath.Join(sessionDir, "worker.json"): network.WorkerRecord{ID: id, Address: address, Secret: secret},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := testHome()
	h.agentpackHome = homeDir
	h.selectedSession().info.Transport = "acp"
	return h
}
func TestParseHubConversationConfigAndLegacyFallback(t *testing.T) {
	options, err := parseHubConversationConfig([]byte(`{"configOptions":[{"id":"model","name":"Model","category":"model","currentValue":"m1","options":[{"value":"m1","name":"Model 1"},{"value":"m2","name":"Model 2"}]}]}`))
	if err != nil || len(options) != 1 || options[0].Category != "model" || options[0].CurrentValue != "m1" || len(options[0].Options) != 2 {
		t.Fatalf("config options = %+v, err = %v", options, err)
	}
	fallback, err := parseHubConversationConfig([]byte(`{"modes":{"currentModeId":"ask","availableModes":[{"id":"ask","name":"Ask"},{"id":"build","name":"Build"}]},"models":{"currentModelId":"m1","availableModels":[{"id":"m1","name":"Model 1"}]}}`))
	if err != nil || len(fallback) != 2 || fallback[0].Category != "mode" || fallback[1].Category != "model" {
		t.Fatalf("legacy config fallback = %+v, err = %v", fallback, err)
	}
	empty, err := parseHubConversationConfig([]byte(`{"configOptions":[],"modes":{"availableModes":[{"id":"ask","name":"Ask"}]}}`))
	if err != nil || len(empty) != 0 {
		t.Fatalf("present empty configOptions unexpectedly fell back: %+v, err = %v", empty, err)
	}
}

func TestHubSetsAgentConfigOptionThroughGateway(t *testing.T) {
	var payload struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/session-aaa/conversation/config" || r.URL.Query().Get("node") != "peer-7" {
			http.Error(w, "unexpected config request", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	h.selectedSession().machine = "peer-7"
	h.previewConfigOptions = []hubConfigOption{{
		ID: "model", Category: "model", Options: []hubConfigChoice{{Value: "m1"}, {Value: "m2"}},
	}}
	h.configCategory, h.configCursor = "model", 1
	result := h.setConfigChoiceCmd()().(configSetMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if payload.Key != "model" || string(payload.Value) != `"m2"` {
		t.Fatalf("config request body = %+v", payload)
	}
}
func TestHubForksOnlyAdvertisedACPConversation(t *testing.T) {
	requests := 0
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/session-aaa/fork" || r.URL.Query().Get("node") != "peer-7" {
			http.Error(w, "unexpected fork request", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(network.SessionInfo{ID: "session-forked", Transport: "acp"})
	}))
	session := h.selectedSession()
	session.machine = "peer-7"
	if result := h.forkSelectedSessionCmd()().(forkedSessionMsg); result.err == nil || requests != 0 {
		t.Fatalf("unadvertised fork result = %+v, requests=%d", result, requests)
	}
	session.info.AgentSessionID = "native-session"
	session.info.AgentCapabilities = &network.AgentCapabilities{
		SessionCapabilities: &network.AgentSessionCapabilities{Fork: &struct{}{}},
	}
	result := h.forkSelectedSessionCmd()().(forkedSessionMsg)
	if result.err != nil || result.info.ID != "session-forked" || requests != 1 {
		t.Fatalf("advertised fork result = %+v, requests=%d", result, requests)
	}
}

func TestHubSendsConversationPromptThroughGateway(t *testing.T) {
	var payload struct {
		Text string `json:"text"`
	}
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/session-aaa/conversation/prompt" || r.URL.Query().Get("node") != "peer-7" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
	}))
	h.selectedSession().machine = "peer-7"
	msg := h.sendPreviewPromptCmd("  summarize this  ", "")().(conversationSentMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if payload.Text != "summarize this" {
		t.Fatalf("prompt text = %q", payload.Text)
	}
}

func TestHubUploadsImageAndSendsPromptReference(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var uploaded []byte
	var prompt struct {
		Text  string `json:"text"`
		Image struct {
			SHA string `json:"sha"`
		} `json:"image"`
	}
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "peer-7" {
			t.Errorf("node query = %q", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/api/sessions/session-aaa/conversation/blobs":
			uploaded, _ = io.ReadAll(r.Body)
			_, _ = io.WriteString(w, `{"sha":"`+sha+`"}`)
		case "/api/sessions/session-aaa/conversation/prompt":
			if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	h.selectedSession().machine = "peer-7"
	image := filepath.Join(t.TempDir(), "image.png")
	content := []byte("small test image")
	if err := os.WriteFile(image, content, 0o600); err != nil {
		t.Fatal(err)
	}
	msg := h.sendPreviewPromptCmd("describe this", image)().(conversationSentMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if string(uploaded) != string(content) || prompt.Text != "describe this" || prompt.Image.SHA != sha {
		t.Fatalf("uploaded=%q prompt=%+v", uploaded, prompt)
	}
}

func TestCacheChatImageFetchesAndReusesVerifiedBlob(t *testing.T) {
	content := []byte("image bytes")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	requests := 0
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/sessions/session-aaa/blobs/") || r.URL.Query().Get("node") != "peer-7" {
			http.NotFound(w, r)
			return
		}
		requests++
		_, _ = w.Write(content)
	}))
	h.selectedSession().machine = "peer-7"
	cacheDir := t.TempDir()
	ref := chatImageRef{SHA: sha, MIME: "image/png"}
	first, err := cacheChatImage(context.Background(), h.agentpackHome, "session-aaa", "peer-7", ref, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cacheChatImage(context.Background(), h.agentpackHome, "session-aaa", "peer-7", ref, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(first)
	if err != nil || string(got) != string(content) || second != first || requests != 1 {
		t.Fatalf("cache=%q second=%q requests=%d err=%v", got, second, requests, err)
	}
	if _, err := cacheChatImage(context.Background(), h.agentpackHome, "session-aaa", "peer-7", chatImageRef{SHA: strings.Repeat("0", 64), MIME: "image/png"}, cacheDir); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("mismatched image was accepted: %v", err)
	}
}

func TestHubAnswersPeerPermissionAndElicitation(t *testing.T) {
	var permission, elicitation map[string]string
	h := conversationTestHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "peer-7" {
			t.Errorf("node query = %q", r.URL.RawQuery)
		}
		var target *map[string]string
		switch r.URL.Path {
		case "/api/sessions/session-aaa/conversation/permission":
			target = &permission
		case "/api/sessions/session-aaa/conversation/elicitation":
			target = &elicitation
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(target); err != nil {
			t.Error(err)
		}
	}))
	h.selectedSession().machine = "peer-7"
	h.previewPerm.requestID = "permission-1"
	if msg := h.answerPermissionViaGateway("allow")().(conversationSentMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	h.previewPerm = pendingPermission{}
	h.previewElicit.id = "question-2"
	if msg := h.answerElicitationViaGateway("yes")().(conversationSentMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	if permission["request_id"] != "permission-1" || permission["option_id"] != "allow" {
		t.Fatalf("permission answer = %#v", permission)
	}
	if elicitation["elicitation_id"] != "question-2" || elicitation["value"] != "yes" {
		t.Fatalf("elicitation answer = %#v", elicitation)
	}
}
func TestPruneChatImageCacheIsBoundedAndScoped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, strings.Repeat("1", 64)+".png")
	keep := filepath.Join(dir, strings.Repeat("2", 64)+".png")
	unowned := filepath.Join(dir, "notes.bin")
	for path, modTime := range map[string]time.Time{
		old:     now.Add(-2 * time.Hour),
		keep:    now,
		unowned: now.Add(-3 * time.Hour),
	} {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxHubImageCacheBytes/2 + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	pruneChatImageCache(dir, keep)
	for path, wantExists := range map[string]bool{old: false, keep: true, unowned: true} {
		_, err := os.Stat(path)
		if (err == nil) != wantExists {
			t.Errorf("%s exists=%v, want %v (err=%v)", filepath.Base(path), err == nil, wantExists, err)
		}
	}
}
