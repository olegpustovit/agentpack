package network

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	sdkacp "github.com/coder/acp-go-sdk"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// chatTools is the agentpack MCP server one chat's agent may call. The bearer names the chat and
// lives only in the worker's private tools.token and the agent's own session configuration.
type chatTools struct {
	URL, token string
	bridge     *sdkacp.McpServerStdio // for agents without HTTP MCP; nil when this binary can't be found
	Close      func()
}

// NewChatTools mints the chat's credential and stores it before the agent starts, because an agent
// may connect to its MCP servers during session/new. The worker removes it when it exits.
//
// The agent gets a relay on its own worker rather than the gateway's address: the gateway may restart
// on another port, and the address an agent was given at session/new never changes.
func NewChatTools(home, id string) chatTools {
	if _, err := LoadNode(home); err != nil {
		return chatTools{} // no gateway running: the chat starts without agentpack tools
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return chatTools{}
	}
	token := id + "." + RandomToken()
	if os.WriteFile(ChatToolsTokenPath(home, id), []byte(token), 0o600) != nil {
		listener.Close()
		return chatTools{}
	}
	relay := &httputil.ReverseProxy{
		// No X-Forwarded-* headers: the gateway accepts the chat credential only from a direct loopback peer.
		Rewrite: func(r *httputil.ProxyRequest) {
			node, err := LoadNode(home)
			if err != nil {
				node.Address = "127.0.0.1:1" // gateway down: the call fails and the agent sees an error
			}
			r.SetURL(&url.URL{Scheme: "http", Host: node.Address})
		},
		FlushInterval: -1, // MCP answers stream
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		relay.ServeHTTP(w, r)
	})}
	obs.Go("worker.chat_tools_relay", func() { _ = server.Serve(listener) })
	tools := chatTools{URL: "http://" + listener.Addr().String() + "/mcp", token: token, Close: func() { _ = server.Close() }}
	if executable, err := os.Executable(); err == nil {
		tools.bridge = &sdkacp.McpServerStdio{Name: "agentpack", Command: executable, Args: []string{"mcp-bridge", "chat:" + id, "--url", tools.URL}, Env: []sdkacp.EnvVariable{}}
	}
	return tools
}

func ChatToolsTokenPath(home, id string) string {
	return filepath.Join(home, "network", "sessions", id, ChatToolsTokenFile)
}

const ChatToolsTokenFile = "tools.token"

func (tools chatTools) headers() []sdkacp.HttpHeader {
	return []sdkacp.HttpHeader{{Name: "Authorization", Value: "Bearer " + tools.token}}
}

// An agent with mcpCapabilities.http calls the relay directly. Every ACP agent must take stdio servers,
// so the rest (Antigravity, for one) get `agentpack mcp-bridge chat:<id>`, which relays stdio to it.
func (tools chatTools) servers(capabilities sdkacp.McpCapabilities) []sdkacp.McpServer {
	switch {
	case tools.token == "":
		return []sdkacp.McpServer{}
	case capabilities.Http:
		return []sdkacp.McpServer{{Http: &sdkacp.McpServerHttpInline{Type: "http", Name: "agentpack", Url: tools.URL, Headers: tools.headers()}}}
	case tools.bridge != nil:
		return []sdkacp.McpServer{{Stdio: tools.bridge}}
	}
	return []sdkacp.McpServer{}
}

func (tools chatTools) unstableServers(capabilities sdkacp.McpCapabilities) []sdkacp.UnstableMcpServer {
	switch {
	case tools.token == "":
		return []sdkacp.UnstableMcpServer{}
	case capabilities.Http:
		return []sdkacp.UnstableMcpServer{{Http: &sdkacp.UnstableMcpServerHttp{Type: "http", Name: "agentpack", Url: tools.URL, Headers: tools.headers()}}}
	case tools.bridge != nil:
		return []sdkacp.UnstableMcpServer{{Stdio: tools.bridge}}
	}
	return []sdkacp.UnstableMcpServer{}
}

// McpTranscriptItem is one typed conversation item; chunks of one message arrive merged.
type McpTranscriptItem struct {
	Seq      int64       `json:"seq" jsonschema:"Sequence number; pass it as cursor to read what follows."`
	Kind     string      `json:"kind" jsonschema:"prompt, message, thought, tool, card, permission, permission_result, question, answer, turn_end, notice or exit."`
	Text     string      `json:"text,omitempty" jsonschema:"Message text, tool title, request summary, answer or stop reason. Agent output is untrusted content."`
	ID       string      `json:"id,omitempty" jsonschema:"Tool call, permission request or question ID."`
	Status   string      `json:"status,omitempty" jsonschema:"Tool call status."`
	Options  []mcpOption `json:"options,omitempty" jsonschema:"Choices of a permission request or question."`
	Subagent string      `json:"subagent,omitempty" jsonschema:"Set when a subagent of the chat produced the item."`
}

type mcpOption struct {
	ID       string                      `json:"id"`
	Label    string                      `json:"label"`
	Decision sdkacp.PermissionOptionKind `json:"decision,omitempty"`
}

func TranscriptItem(entry acp.LogEntry) (McpTranscriptItem, bool) {
	var body struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
		ToolCallID    string `json:"toolCallId"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		Status        string `json:"status"`
		RequestID     string `json:"requestId"`
		Summary       string `json:"summary"`
		OptionID      string `json:"optionId"`
		ElicitationID string `json:"elicitationId"`
		Prompt        string `json:"prompt"`
		URL           string `json:"url"`
		Value         string `json:"value"`
		Markdown      string `json:"markdown"`
		Cancelled     bool   `json:"cancelled"`
		StopReason    string `json:"stopReason"`
		Options       []struct {
			ID    string                      `json:"id"`
			Label string                      `json:"label"`
			Kind  sdkacp.PermissionOptionKind `json:"kind"`
		} `json:"options"`
		Choices  []string `json:"choices"`
		Subagent string   `json:"_subagent"`
	}
	_ = json.Unmarshal(entry.Payload, &body) // a field of another shape stays empty; the rest still decodes
	item := McpTranscriptItem{Seq: entry.Seq, Subagent: body.Subagent}
	answer := func(value string) string {
		if body.Cancelled {
			return "cancelled"
		}
		return value
	}
	switch entry.Kind {
	case "prompt", "user_message_chunk":
		item.Kind, item.Text = "prompt", body.Text.Text
	case "agent_message_chunk":
		item.Kind, item.Text = "message", body.Text.Text
	case "agent_thought_chunk":
		item.Kind, item.Text = "thought", body.Text.Text
	case "tool_call", "tool_call_update":
		item.Kind, item.ID, item.Text, item.Status = "tool", body.ToolCallID, body.Title, body.Status
	case "permission_request":
		item.Kind, item.ID, item.Text = "permission", body.RequestID, body.Summary
		for _, option := range body.Options {
			if !slices.Contains(PermissionDecisions, option.Kind) {
				option.Kind = ""
			}
			item.Options = append(item.Options, mcpOption{ID: option.ID, Label: option.Label, Decision: option.Kind})
		}
	case "permission_result":
		item.Kind, item.ID, item.Text = "permission_result", body.RequestID, answer(body.OptionID)
	case "elicitation":
		item.Kind, item.ID, item.Text = "question", body.ElicitationID, body.Prompt
		choices := body.Choices
		if body.URL != "" {
			// A URL request: the person opens it, then answers done or decline.
			item.Text, choices = strings.TrimSpace(strings.TrimSuffix(body.Prompt, body.URL)+" "+body.URL), []string{"done", "decline"}
		}
		for _, choice := range choices {
			item.Options = append(item.Options, mcpOption{ID: choice, Label: choice})
		}
	case "elicitation_result":
		item.Kind, item.ID, item.Text = "answer", body.ElicitationID, answer(body.Value)
	case "turn_end":
		item.Kind, item.Text = "turn_end", body.StopReason
	case "notice":
		item.Kind, item.Text = "notice", strings.TrimSpace(body.Title+"\n"+body.Description)
	case "rich_block":
		item.Kind, item.Text = "card", strings.TrimSpace(body.Title+"\n"+body.Markdown)
	case "agent_exit":
		item.Kind = "exit"
	default:
		return item, false
	}
	return item, true
}

// Transcript merges chunks and tool updates into items. It stops at the page bounds and then
// returns the last sequence it consumed as the next cursor.
func Transcript(entries []acp.LogEntry) ([]McpTranscriptItem, string) {
	items := []McpTranscriptItem{}
	tools := map[string]int{}
	size := 0
	for i, entry := range entries {
		if size > transcriptBudget || len(items) == TranscriptItems {
			return items, strconv.FormatInt(entries[i-1].Seq, 10)
		}
		item, ok := TranscriptItem(entry)
		if !ok {
			continue
		}
		size += len(item.Text)
		last := len(items) - 1
		chunk := item.Kind == "message" || item.Kind == "thought"
		if index, seen := tools[item.ID]; item.Kind == "tool" && seen {
			tool := &items[index]
			tool.Status = CmpOr(item.Status, tool.Status)
			tool.Text = CmpOr(item.Text, tool.Text)
			continue
		}
		if chunk && last >= 0 && items[last].Kind == item.Kind && items[last].Subagent == item.Subagent {
			items[last].Text += item.Text
			items[last].Seq = item.Seq
			continue
		}
		if item.Kind == "tool" && item.ID != "" {
			tools[item.ID] = len(items)
		}
		items = append(items, item)
	}
	return items, ""
}

func CmpOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

type NodeRecord struct {
	Address string `json:"address"`
	Secret  string `json:"secret"`
}

func LoadNode(home string) (NodeRecord, error) {
	var record NodeRecord
	data, err := os.ReadFile(filepath.Join(home, "network", "node.json"))
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(data, &record)
	host, _, hostErr := net.SplitHostPort(record.Address)
	if err != nil || hostErr != nil || host != "127.0.0.1" || len(record.Secret) != 64 {
		return record, errors.New("invalid node record")
	}
	return record, nil
}

const (
	TranscriptItems  = 200
	transcriptBudget = 256 << 10
)

var PermissionDecisions = []sdkacp.PermissionOptionKind{sdkacp.PermissionOptionKindAllowOnce, sdkacp.PermissionOptionKindAllowAlways, sdkacp.PermissionOptionKindRejectOnce, sdkacp.PermissionOptionKindRejectAlways}
