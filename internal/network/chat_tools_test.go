package network

import (
	"context"
	"strings"
	"testing"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp/acptest"
)

func TestChatGetsAgentpackToolsOverHTTPOrTheStdioBridge(t *testing.T) {
	bridge := &sdkacp.McpServerStdio{Name: "agentpack", Command: "/bin/agentpack", Args: []string{"mcp-bridge", "chat:session-x", "--url", "http://127.0.0.1:7420/mcp"}, Env: []sdkacp.EnvVariable{}}
	tools := chatTools{URL: "http://127.0.0.1:7420/mcp", token: "session-x.secret", bridge: bridge}
	for _, http := range []bool{true, false} {
		agent := &authReplayAgent{ReplayAgent: acptest.NewReplayAgent(nil), agentCapabilities: sdkacp.AgentCapabilities{McpCapabilities: sdkacp.McpCapabilities{Http: http}}}
		connection, cleanup := authReplayConnection(agent)
		if _, err := initializeACPSession(context.Background(), connection, t.TempDir(), "", "", "", tools, nil); err != nil {
			t.Fatal(err)
		}
		cleanup()
		servers := agent.newRequest.McpServers
		if !http {
			// Every ACP agent takes stdio servers; the token stays in the chat's private file.
			if len(servers) != 1 || servers[0].Stdio == nil || servers[0].Stdio.Command != bridge.Command || strings.Join(servers[0].Stdio.Args, " ") != strings.Join(bridge.Args, " ") || strings.Contains(strings.Join(servers[0].Stdio.Args, " "), "secret") {
				t.Fatalf("agent without HTTP MCP: %+v", servers)
			}
			continue
		}
		if len(servers) != 1 || servers[0].Http == nil || servers[0].Http.Name != "agentpack" || servers[0].Http.Url != tools.URL ||
			servers[0].Http.Headers[0].Value != "Bearer session-x.secret" {
			t.Fatalf("mcpServers: %+v", servers)
		}
	}
}
