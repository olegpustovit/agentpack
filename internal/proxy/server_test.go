package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type staticAuth struct {
	endpoint  string
	refreshed atomic.Bool
}

func (auth *staticAuth) Snapshot() (AuthSnapshot, error) {
	return AuthSnapshot{AccessToken: "upstream-token", AccountID: "acct_1", EndpointURL: auth.endpoint}, nil
}
func (auth *staticAuth) RefreshAfterUnauthorized() (bool, error) {
	auth.refreshed.Store(true)
	return true, nil
}

func TestServerTranslatesAndReducesMessages(t *testing.T) {
	var upstreamBody map[string]any
	fixture, err := os.ReadFile("testdata/codex_text.sse")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses" {
			t.Errorf("path=%s", request.URL.Path)
		}
		if request.Header.Get("chatgpt-account-id") != "acct_1" {
			t.Errorf("account header missing")
		}
		_ = json.NewDecoder(request.Body).Decode(&upstreamBody)
		writer.Header().Set("content-type", "text/event-stream")
		_, _ = writer.Write(fixture)
	}))
	defer upstream.Close()
	config := Config{ClientToken: "client-token", Transport: TransportHTTP, RequestTimeout: 30 * time.Second, Models: DefaultModelMap(), Diagnostics: DiagnosticsConfig{}}
	server, err := NewServer(config, &staticAuth{endpoint: upstream.URL + "/responses"})
	if err != nil {
		t.Fatal(err)
	}
	done := server.Start()
	defer func() { _ = server.Shutdown(context.Background()); <-done }()
	request, _ := http.NewRequest(http.MethodPost, server.BaseURL()+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`))
	request.Header.Set("authorization", "Bearer client-token")
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("status=%s body=%s", response.Status, body)
	}
	var value map[string]any
	_ = json.Unmarshal(body, &value)
	if stringValue(object(array(value["content"])[0])["text"]) != "Hello world" {
		t.Fatalf("body=%s", body)
	}
	if stringValue(upstreamBody["model"]) != "gpt-5.4" || !boolean(upstreamBody["stream"]) {
		t.Fatalf("upstream=%#v", upstreamBody)
	}
}

func TestServerRejectsInvalidClientToken(t *testing.T) {
	config := Config{ClientToken: "right", Transport: TransportHTTP, RequestTimeout: time.Second, Models: DefaultModelMap()}
	server, err := NewServer(config, &staticAuth{endpoint: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	done := server.Start()
	defer func() { _ = server.Shutdown(context.Background()); <-done }()
	request, _ := http.NewRequest(http.MethodPost, server.BaseURL()+"/v1/messages/count_tokens", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("authorization", "Bearer wrong")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestRunningAppliesClaudeEnvironmentWithoutAPIKey(t *testing.T) {
	server, err := NewServer(Config{Models: DefaultModelMap()}, &staticAuth{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.listener.Close()
	running := &Running{Server: server, Token: "proxy-token"}
	command := exec.Command("claude")
	command.Env = []string{"ANTHROPIC_API_KEY=old", "ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5.4"}
	running.Apply(command)
	joined := strings.Join(command.Env, "\n")
	if strings.Contains(joined, "ANTHROPIC_API_KEY=") || !strings.Contains(joined, "ANTHROPIC_AUTH_TOKEN=proxy-token") {
		t.Fatalf("env=%v", command.Env)
	}
	// A model the user chose wins over the proxy's family default.
	if strings.Count(joined, "ANTHROPIC_DEFAULT_OPUS_MODEL=") != 1 || !strings.Contains(joined, "ANTHROPIC_DEFAULT_OPUS_MODEL=gpt-5.4") || !strings.Contains(joined, "ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-4-6") {
		t.Fatalf("env=%v", command.Env)
	}
}

// The proxy forwards each upstream event as it arrives, and a failure after the first byte
// arrives as an Anthropic error event because the status is already sent.
func TestServerStreamsBeforeUpstreamEnds(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"m\"}}\n\n"+
			"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hel\"}\n\n")
		writer.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\"}}}\n\n")
	}))
	defer upstream.Close()
	defer close(release)
	server, err := NewServer(Config{Transport: TransportHTTP, RequestTimeout: 30 * time.Second, Models: DefaultModelMap()}, &staticAuth{endpoint: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	done := server.Start()
	defer func() { _ = server.Shutdown(context.Background()); <-done }()
	// The deadline only ends a buffering proxy's hang; a streaming one answers at once.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.BaseURL()+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var head strings.Builder
	for !strings.Contains(head.String(), `"text":"Hel"`) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the first delta: %v %q", err, head.String())
		}
		head.WriteString(line)
	}
	release <- struct{}{}
	rest, _ := io.ReadAll(reader)
	if response.StatusCode != 200 || !strings.Contains(string(rest), "event: error") || !strings.Contains(string(rest), "boom") {
		t.Fatalf("status %d rest %q", response.StatusCode, rest)
	}
}
