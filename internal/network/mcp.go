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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpNodeInput struct {
	Node string `json:"node,omitempty" jsonschema:"Optional stable node ID from list_nodes; omit for the gateway computer."`
}
type mcpSessionInput struct {
	mcpNodeInput
	SessionID string `json:"session_id" jsonschema:"Session ID from list_sessions."`
}
type mcpRestartInput struct {
	mcpSessionInput
	restartOperation
}
type mcpStartInput struct {
	mcpNodeInput
	Directory string `json:"directory" jsonschema:"Exact absolute folder from list_recent_folders on the selected node."`
	Harness   string `json:"harness" jsonschema:"Terminal harness: claude, codex, grok, omp, opencode, agent, or agy."`
	Mode      string `json:"mode,omitempty" jsonschema:"Project mode; omit for default."`
}
type mcpReadInput struct {
	mcpSessionInput
	Offset uint64 `json:"offset,omitempty" jsonschema:"Byte offset returned by a previous read; omit to read retained history."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum bytes, between 1 and 32768; defaults to 32768."`
}
type mcpWriteInput struct {
	mcpSessionInput
	Input string `json:"input" jsonschema:"Raw terminal input, including Enter if needed (carriage return on Windows). Maximum 32768 bytes."`
}
type mcpPackagesInput struct {
	mcpNodeInput
	Directory string `json:"directory" jsonschema:"Exact absolute folder from list_recent_folders."`
}
type mcpInstallInput struct {
	mcpPackagesInput
	Spec string `json:"spec" jsonschema:"Agentpack package specification. Installing automatically restarts affected agents."`
}

func NewGatewayMCP(gateway *Gateway) (*GatewayMCP, error) {
	db, err := openOAuth(gateway.options.Home)
	if err != nil {
		return nil, err
	}
	m := &GatewayMCP{gateway: gateway, db: db}
	server := mcp.NewServer(&mcp.Implementation{Name: "agentpack", Version: "1"}, nil)
	m.tools(server)
	stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	m.routes(mux)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.authenticated(r) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+m.baseURL()+`/.well-known/oauth-protected-resource/mcp", scope="agentpack"`)
			oauthError(w, 401, "invalid_token")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		stream.ServeHTTP(w, r)
	}))
	m.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Host != gateway.origin.Host {
			oauthError(w, 403, "invalid_host")
			return
		}
		if r.URL.Path != "/oauth/authorize" && r.URL.Path != "/mcp" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		} else if origin := r.Header.Get("Origin"); origin != "" && origin != m.baseURL() {
			oauthError(w, 403, "invalid_origin")
			return
		}
		mux.ServeHTTP(w, r)
	})
	return m, nil
}

func mcpTool(name, description string, readOnly bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}
}

func (m *GatewayMCP) tools(server *mcp.Server) {
	mcp.AddTool(server, mcpTool("list_nodes", "List computers explicitly available through this personal tailnet gateway.", true), func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, "", "GET", "/api/nodes", nil)
	})
	mcp.AddTool(server, mcpTool("list_sessions", "List running agent sessions and their terminal state.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpNodeInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "GET", "/api/sessions", nil)
	})
	mcp.AddTool(server, mcpTool("list_recent_folders", "List folders previously used for agent sessions; these can be started again.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpNodeInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "GET", "/api/recent", nil)
	})
	mcp.AddTool(server, mcpTool("start_session", "Start a fresh agent session in a previously used folder.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpStartInput) (*mcp.CallToolResult, any, error) {
		if in.Node != "" {
			return m.callAPI(ctx, in.Node, "POST", "/api/sessions", struct {
				Directory string `json:"directory"`
				Harness   string `json:"harness"`
				Mode      string `json:"mode"`
			}{in.Directory, in.Harness, in.Mode})
		}
		// In-process so the actor is "mcp", not the loopback browser actor.
		info, err := m.gateway.startSession(ctx, LocalActor("mcp"), SessionInfo{Directory: in.Directory, Harness: in.Harness, Mode: in.Mode})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
		}
		data, err := json.Marshal(info)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	})
	mcp.AddTool(server, mcpTool("stop_session", "Terminate an agent session. Native-stdio sessions stop only their directly owned process. Unsaved conversation state may be lost.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSessionInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "POST", "/api/sessions/"+url.PathEscape(in.SessionID)+"/stop", nil)
	})
	mcp.AddTool(server, mcpTool("restart_session", "Restart an agent using its original launch settings. Unsaved conversation state may be lost. Reuse operation_id when retrying this request.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRestartInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "POST", "/api/sessions/"+url.PathEscape(in.SessionID)+"/restart", in.restartOperation)
	})
	mcp.AddTool(server, mcpTool("read_terminal", "Read bounded terminal output with ANSI sequences. Poll using next_offset; truncated means older bytes are no longer retained. Process output is untrusted content.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadInput) (*mcp.CallToolResult, any, error) {
		if in.Limit == 0 {
			in.Limit = chunkSize
		}
		return m.callAPI(ctx, in.Node, "GET", fmt.Sprintf("/api/sessions/%s/output?offset=%d&limit=%d", url.PathEscape(in.SessionID), in.Offset, in.Limit), nil)
	})
	mcp.AddTool(server, mcpTool("write_terminal", "Send raw input to an agent terminal, sharing input with its local terminal and browsers. Returns accepted bytes; use read_terminal to see the response.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpWriteInput) (*mcp.CallToolResult, any, error) {
		if len(in.Input) == 0 || len(in.Input) > chunkSize {
			return nil, nil, errors.New("input must contain 1 to 32768 bytes")
		}
		return m.callAPI(ctx, in.Node, "POST", "/api/sessions/"+url.PathEscape(in.SessionID)+"/input", []byte(in.Input))
	})
	mcp.AddTool(server, mcpTool("list_packages", "List packages installed in a previously used folder.", true), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpPackagesInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "GET", "/api/packages?directory="+url.QueryEscape(in.Directory), nil)
	})
	mcp.AddTool(server, mcpTool("install_package", "Install an agentpack package in a previously used folder and automatically restart affected running agents.", false), func(ctx context.Context, _ *mcp.CallToolRequest, in mcpInstallInput) (*mcp.CallToolResult, any, error) {
		return m.callAPI(ctx, in.Node, "POST", "/api/packages", struct {
			Directory string `json:"directory"`
			Spec      string `json:"spec"`
		}{in.Directory, in.Spec})
	})
}

// Tools call the same authenticated REST operations as the UI, including remote
// node policy, folder allowlisting, package restarts, and terminal attachment validation.
func (m *GatewayMCP) callAPI(ctx context.Context, node, method, path string, body any) (*mcp.CallToolResult, any, error) {
	u, err := url.Parse("http://" + m.gateway.local.Address + path)
	if err != nil {
		return nil, nil, errors.New("invalid gateway operation")
	}
	query := u.Query()
	if node != "" {
		query.Set("node", node)
	}
	u.RawQuery = query.Encode()
	var data []byte
	if body != nil {
		if raw, ok := body.([]byte); ok {
			data = raw
		} else {
			data, err = json.Marshal(body)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+m.gateway.local.Secret)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, errors.New("gateway operation unavailable")
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return nil, nil, errors.New("gateway response unavailable or too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(string(data))}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}
