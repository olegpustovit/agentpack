package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPBridgeOptions is one `agentpack mcp-bridge` run: a stdio MCP server for
// the harness that adds a stored credential, fetched from the local gateway,
// to a remote server's requests (URL) or a local server's environment (Command).
type MCPBridgeOptions struct {
	Home, ID, URL string
	SSE           bool
	Headers       map[string]string
	Command       []string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

// bridgeCredential caches the secret for one bridge; the gateway decides when a token needs refreshing.
type bridgeCredential struct {
	home, id string
	mu       sync.Mutex
	secret   *mcpBridgeSecret
}

func (c *bridgeCredential) request(ctx context.Context, method, path string, body io.Reader) (mcpBridgeSecret, error) {
	data, err := NodeRequest(ctx, c.home, method, "/api/mcp/credentials/"+c.id+path, body)
	if err != nil {
		if _, nodeErr := loadNode(c.home); nodeErr != nil {
			return mcpBridgeSecret{}, errors.New("the agentpack gateway is not running; run `ap`, then retry")
		}
		if strings.Contains(err.Error(), "needs login") {
			return mcpBridgeSecret{}, errors.New("this MCP credential needs login; reconnect it in the gateway under Settings → Connections")
		}
		return mcpBridgeSecret{}, fmt.Errorf("MCP credential unavailable: %w", err)
	}
	var secret mcpBridgeSecret
	if err := json.Unmarshal(data, &secret); err != nil {
		return secret, errors.New("MCP credential unavailable: unreadable gateway answer")
	}
	if secret.Status == mcpStatusNeedsLogin {
		return secret, fmt.Errorf("MCP credential %q needs login; reconnect it at %s", secret.Label, secret.ConnectURL)
	}
	return secret, nil
}

func (c *bridgeCredential) get(ctx context.Context) (mcpBridgeSecret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secret != nil && usable(*c.secret, time.Now()) {
		return *c.secret, nil
	}
	secret, err := c.request(ctx, http.MethodGet, "/secret", nil)
	if err != nil {
		return secret, err
	}
	c.secret = &secret
	return secret, nil
}

// refresh asks for a token newer than the one the server rejected; concurrent rejections refresh once.
func (c *bridgeCredential) refresh(ctx context.Context, rejected int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secret != nil && c.secret.Generation > rejected {
		return nil
	}
	body, _ := json.Marshal(map[string]int64{"generation": rejected})
	secret, err := c.request(ctx, http.MethodPost, "/refresh", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.secret = &secret
	return nil
}

// bridgeTransport adds the credential and the negotiated protocol version, and retries a 401 once after a refresh.
type bridgeTransport struct {
	credential *bridgeCredential
	headers    map[string]string
	version    func() string
	base       http.RoundTripper
}

func (t *bridgeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		secret, err := t.credential.get(request.Context())
		if err != nil {
			return nil, err
		}
		out := request.Clone(request.Context())
		if attempt > 0 && request.GetBody != nil {
			if out.Body, err = request.GetBody(); err != nil {
				return nil, err
			}
		}
		for key, value := range t.headers {
			out.Header.Set(key, value)
		}
		for key, value := range secret.Headers {
			out.Header.Set(key, value)
		}
		if version := t.version(); version != "" && out.Header.Get("MCP-Protocol-Version") == "" {
			out.Header.Set("MCP-Protocol-Version", version)
		}
		response, err := t.base.RoundTrip(out)
		if err != nil || response.StatusCode != http.StatusUnauthorized || attempt > 0 || request.Body != nil && request.GetBody == nil {
			return response, err
		}
		response.Body.Close()
		if err := t.credential.refresh(request.Context(), secret.Generation); err != nil {
			return nil, err
		}
	}
}

// RunMCPBridge serves the harness until it closes standard input.
func RunMCPBridge(ctx context.Context, options MCPBridgeOptions) (int, error) {
	credential := &bridgeCredential{home: options.Home, id: options.ID}
	if len(options.Command) != 0 {
		return runEnvBridge(ctx, credential, options)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var versionMu sync.Mutex
	version := ""
	client := &http.Client{Transport: &bridgeTransport{credential: credential, headers: options.Headers, base: mcpOutboundClient().Transport, version: func() string {
		versionMu.Lock()
		defer versionMu.Unlock()
		return version
	}}}
	var transport mcp.Transport = &mcp.StreamableClientTransport{Endpoint: options.URL, HTTPClient: client, MaxEventSize: -1,
		// shortcut: without the SDK's session hook the bridge opens no standalone GET stream, so notifications a server sends outside a request (list_changed) are dropped; add a GET loop if a server relies on them.
		DisableStandaloneSSE: true}
	if options.SSE {
		transport = &mcp.SSEClientTransport{Endpoint: options.URL, HTTPClient: client, MaxEventSize: -1}
	}
	var writeMu sync.Mutex
	emit := func(message jsonrpc.Message) {
		data, err := jsonrpc.EncodeMessage(message)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		options.Stdout.Write(append(data, '\n'))
	}
	fail := func(message jsonrpc.Message, cause error) {
		if request, ok := message.(*jsonrpc.Request); ok && request.IsCall() {
			emit(&jsonrpc.Response{ID: request.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: cause.Error()}})
		}
	}
	var connection mcp.Connection
	var connectErr error
	connected := sync.OnceFunc(func() {
		connection, connectErr = transport.Connect(ctx)
		if connectErr != nil {
			return
		}
		go func() {
			defer cancel()
			for {
				message, err := connection.Read(ctx)
				if err != nil {
					if ctx.Err() == nil {
						fmt.Fprintf(options.Stderr, "agentpack mcp-bridge: %v\n", err)
					}
					return
				}
				if response, ok := message.(*jsonrpc.Response); ok && len(response.Result) != 0 {
					var result struct {
						ProtocolVersion string `json:"protocolVersion"`
					}
					if json.Unmarshal(response.Result, &result) == nil && result.ProtocolVersion != "" {
						versionMu.Lock()
						if version == "" {
							version = result.ProtocolVersion
						}
						versionMu.Unlock()
					}
				}
				emit(message)
			}
		}()
	})
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(options.Stdin)
		for {
			line, err := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) != 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var pending sync.WaitGroup
	defer func() {
		pending.Wait()
		if connection != nil {
			connection.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return 0, nil
		case line, open := <-lines:
			if !open {
				return 0, nil
			}
			message, err := jsonrpc.DecodeMessage(bytes.TrimSpace(line))
			if err != nil {
				fmt.Fprintf(options.Stderr, "agentpack mcp-bridge: unreadable message from the agent: %v\n", err)
				continue
			}
			// The SDK connects lazily on the first write, so the harness's initialize also carries a missing-credential error back.
			connected()
			if connectErr != nil {
				fail(message, connectErr)
				continue
			}
			pending.Add(1)
			go func() {
				defer pending.Done()
				if err := connection.Write(ctx, message); err != nil && ctx.Err() == nil {
					fail(message, err)
				}
			}()
		}
	}
}

// runEnvBridge starts a local server with the credential's variables added to its environment.
func runEnvBridge(ctx context.Context, credential *bridgeCredential, options MCPBridgeOptions) (int, error) {
	secret, err := credential.get(ctx)
	if err != nil {
		return 1, err
	}
	command := exec.CommandContext(ctx, options.Command[0], options.Command[1:]...)
	command.Env = os.Environ()
	for key, value := range secret.Env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdin, command.Stdout, command.Stderr = options.Stdin, options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
