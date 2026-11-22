package network

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A2A: every chat is an A2A agent at <gateway>/a2a/<session> for clients holding a gateway OAuth
// token, and a chat's agent calls other A2A agents with call_agent. Both use agent_tasks: a client's
// task is an 'in' row from a2aNode, a call is an 'out' row to a2aNode.

const (
	a2aBlockMax     = mcpWaitMax       // SendMessage without returnImmediately waits this long for an answer
	a2aPollInterval = 15 * time.Second // how often a called agent's open task is read
	a2aCallTimeout  = 30 * time.Second
)

var a2aStates = map[taskState]a2a.TaskState{
	taskSubmitted: a2a.TaskStateSubmitted, taskWorking: a2a.TaskStateWorking, taskInputRequired: a2a.TaskStateInputRequired,
	taskCompleted: a2a.TaskStateCompleted, taskFailed: a2a.TaskStateFailed, taskCanceled: a2a.TaskStateCanceled, taskRejected: a2a.TaskStateRejected,
}

func fromA2AState(state a2a.TaskState) taskState {
	for ours, theirs := range a2aStates {
		if theirs == state {
			return ours
		}
	}
	if state == a2a.TaskStateAuthRequired {
		return taskFailed // we hold no way to sign in to another agent mid-task
	}
	return taskWorking
}

// serveA2A is the public entry: it authenticates the OAuth token, then hands the JSON-RPC call to
// the machine that runs the chat, as the token's account.
func (m *GatewayMCP) serveA2A(w http.ResponseWriter, r *http.Request) {
	verified := m.authenticate(r)
	if verified == nil {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+m.baseURL(r)+`/.well-known/oauth-protected-resource/mcp", scope="agentpack"`)
		oauthError(w, 401, "invalid_token")
		return
	}
	ctx, id := verified.Context(), r.PathValue("session")
	info, err := storedSession(ctx, m.gateway.auth.store.Reader, id)
	actor, _ := actorFrom(ctx)
	if err != nil || info.Transport != "acp" || !info.ArchivedAt.IsZero() || !actor.canWriteSession(info) {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, a2aCard(m.baseURL(r)+"/a2a/"+url.PathEscape(id), info))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	_, node, err := m.localNode(ctx, info.NodeID)
	if err != nil {
		http.Error(w, "agent unavailable", http.StatusInternalServerError)
		return
	}
	// Headers do not cross the mesh; the SDK also takes the protocol version as a query parameter.
	query := url.Values{a2a.SvcParamVersion: {r.Header.Get(a2a.SvcParamVersion)}}
	response, err := m.do(ctx, node, http.MethodPost, "/api/sessions/"+url.PathEscape(id)+"/a2a?"+query.Encode(), body)
	if err != nil {
		http.Error(w, err.Error(), cmpOrStatus(apiStatus(err), http.StatusBadGateway))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(response.body.Bytes())
}

func cmpOrStatus(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

func a2aCard(endpoint string, info SessionInfo) *a2a.AgentCard {
	name := cmpOr(info.Name, cmpOr(info.Title, "Chat "+info.ID))
	description := fmt.Sprintf("An agentpack %s chat in %s. Messages reach its agent as requests from another agent; it answers when its turn ends.", agentName(info.Harness), path.Base(info.Directory))
	return &a2a.AgentCard{
		Name: name, Description: description, Version: "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills:               []a2a.AgentSkill{{ID: "chat", Name: name, Description: description, Tags: []string{"coding", info.Harness}}},
		SecuritySchemes:      a2a.NamedSecuritySchemes{"agentpack": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer", Description: "An agentpack gateway OAuth access token (scope agentpack)."}},
		SecurityRequirements: a2a.SecurityRequirementsOptions{{"agentpack": a2a.SecuritySchemeScopes{"agentpack"}}},
	}
}

// serveSessionA2A runs the JSON-RPC call on the machine that runs the chat.
func (g *Gateway) serveSessionA2A(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := storedSession(r.Context(), g.auth.store.Reader, id)
	if err != nil || info.Transport != "acp" || g.mcp == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, info) {
		return
	}
	a2asrv.NewJSONRPCHandler(&a2aChat{m: g.mcp, session: id}).ServeHTTP(w, r)
}

// a2aChat serves one chat's A2A tasks from agent_tasks.
type a2aChat struct {
	m       *GatewayMCP
	session string
}

var _ a2asrv.RequestHandler = (*a2aChat)(nil)

type a2aRow struct {
	id, context, request, result string
	state                        taskState
	updated                      int64
}

func (c *a2aChat) row(ctx context.Context, id string) (a2aRow, error) {
	row := a2aRow{id: id}
	err := c.m.gateway.auth.store.Reader.QueryRowContext(ctx, "SELECT context_id,request,result,state,updated_at FROM agent_tasks WHERE id=? AND role='in' AND to_session=? AND from_node=?", id, c.session, a2aNode).
		Scan(&row.context, &row.request, &row.result, &row.state, &row.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return row, a2a.ErrTaskNotFound
	}
	return row, err
}

func (row a2aRow) task() *a2a.Task {
	at := time.Unix(0, row.updated).UTC()
	task := &a2a.Task{ID: a2a.TaskID(row.id), ContextID: row.context, Status: a2a.TaskStatus{State: a2aStates[row.state], Timestamp: &at}}
	request := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(row.request))
	request.TaskID, request.ContextID = task.ID, task.ContextID
	task.History = []*a2a.Message{request}
	if row.result != "" {
		answer := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(row.result))
		answer.TaskID, answer.ContextID = task.ID, task.ContextID
		task.Status.Message = answer
	}
	return task
}

func messageText(message *a2a.Message) string {
	if message == nil {
		return ""
	}
	var parts []string
	for _, part := range message.Parts {
		if text := part.Text(); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (c *a2aChat) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if req == nil || req.Message == nil {
		return nil, a2a.ErrInvalidParams
	}
	text, err := taskText(messageText(req.Message))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", a2a.ErrInvalidParams, err)
	}
	task, contextID := string(req.Message.TaskID), req.Message.ContextID
	if task != "" {
		// Answering the question a task's agent asked.
		row, err := c.row(ctx, task)
		if err != nil {
			return nil, err
		}
		if row.state != taskInputRequired {
			return nil, fmt.Errorf("%w: task %s is not waiting for input", a2a.ErrInvalidParams, task)
		}
		contextID = row.context
	} else {
		var recent int
		if err := c.m.gateway.auth.store.Reader.QueryRowContext(ctx, "SELECT count(*) FROM agent_tasks WHERE role='in' AND to_session=? AND from_node=? AND created_at>?", c.session, a2aNode, time.Now().Add(-time.Minute).UnixNano()).Scan(&recent); err != nil {
			return nil, err
		}
		if recent >= maxAsksPerMinute {
			return nil, fmt.Errorf("%w: %d messages in the last minute; wait for answers", a2a.ErrUnsupportedOperation, maxAsksPerMinute)
		}
		task = newTaskID()
		if !validRef(contextID, true) {
			return nil, fmt.Errorf("%w: invalid contextId", a2a.ErrInvalidParams)
		}
	}
	actor, _ := actorFrom(ctx)
	message := agentMessage{Task: task, Context: cmpOr(contextID, task), Kind: "ask", FromSession: a2aNode, FromNode: a2aNode, FromName: cmpOr(actor.User, "A2A client"), FromAgent: a2aNode,
		Text: text, Operation: task + "-a-" + strconv.FormatInt(time.Now().UnixNano(), 10)}
	if err := c.m.deliver(ctx, "", c.session, message, mcpWaitMax); err != nil {
		return nil, err
	}
	row, err := c.row(ctx, task)
	deadline := time.Now().Add(a2aBlockMax)
	for err == nil && row.state.open() && (req.Config == nil || !req.Config.ReturnImmediately) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(mcpWaitPoll):
		}
		row, err = c.row(ctx, task)
	}
	if err != nil {
		return nil, err
	}
	return row.task(), nil
}

func (c *a2aChat) GetTask(ctx context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	row, err := c.row(ctx, string(req.ID))
	if err != nil {
		return nil, err
	}
	return row.task(), nil
}

func (c *a2aChat) ListTasks(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	status := ""
	for ours, theirs := range a2aStates {
		if req.Status != "" && theirs == req.Status {
			status = string(ours)
		}
	}
	rows, err := c.m.gateway.auth.store.Reader.QueryContext(ctx, `SELECT id,context_id,request,result,state,updated_at FROM agent_tasks WHERE role='in' AND to_session=? AND from_node=?
 AND (?='' OR context_id=?) AND (?='' OR state=?) ORDER BY updated_at DESC LIMIT ?`, c.session, a2aNode, req.ContextID, req.ContextID, status, status, mcpPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &a2a.ListTasksResponse{Tasks: []*a2a.Task{}}
	for rows.Next() {
		var row a2aRow
		if err := rows.Scan(&row.id, &row.context, &row.request, &row.result, &row.state, &row.updated); err != nil {
			return nil, err
		}
		out.Tasks = append(out.Tasks, row.task())
	}
	out.TotalSize, out.PageSize = len(out.Tasks), mcpPageSize
	return out, rows.Err()
}

func (c *a2aChat) CancelTask(ctx context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	row, err := c.row(ctx, string(req.ID))
	if err != nil {
		return nil, err
	}
	if !row.state.open() && row.state != taskInputRequired {
		return nil, a2a.ErrTaskNotCancelable
	}
	if err := c.m.deliver(ctx, "", c.session, agentMessage{Task: row.id, Kind: "cancel", FromSession: a2aNode, FromNode: a2aNode}, 0); err != nil {
		return nil, err
	}
	return c.GetTask(ctx, &a2a.GetTaskRequest{ID: req.ID})
}

func (c *a2aChat) SubscribeToTask(context.Context, *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return unsupportedEvents
}

func (c *a2aChat) SendStreamingMessage(context.Context, *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return unsupportedEvents
}

func unsupportedEvents(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }

func (c *a2aChat) GetTaskPushConfig(context.Context, *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (c *a2aChat) ListTaskPushConfigs(context.Context, *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (c *a2aChat) CreateTaskPushConfig(context.Context, *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, a2a.ErrPushNotificationNotSupported
}

func (c *a2aChat) DeleteTaskPushConfig(context.Context, *a2a.DeleteTaskPushConfigRequest) error {
	return a2a.ErrPushNotificationNotSupported
}

func (c *a2aChat) GetExtendedAgentCard(context.Context, *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, a2a.ErrExtendedCardNotConfigured
}

// bearerTransport adds an A2A agent's token to every request to it.
type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// a2aClient resolves an agent's card at base and connects to it.
func a2aClient(ctx context.Context, base, token string) (*a2aclient.Client, *a2a.AgentCard, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return nil, nil, errors.New("agent_url is the agent's http(s) base URL")
	}
	httpClient := &http.Client{Timeout: a2aCallTimeout, Transport: bearerTransport{token}}
	card, err := agentcard.NewResolver(httpClient).Resolve(ctx, base)
	if err != nil {
		return nil, nil, fmt.Errorf("read the agent card: %w", err)
	}
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(httpClient))
	return client, card, err
}

func taskAnswer(task *a2a.Task) string {
	parts := []string{messageText(task.Status.Message)}
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			parts = append(parts, part.Text())
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// callAgent sends text to an A2A agent; an answer that is not ready wakes this chat later.
func (m *GatewayMCP) callAgent(ctx context.Context, base, text, contextID, token string) (mcpAsked, error) {
	self, local, err := m.caller(ctx)
	if err != nil {
		return mcpAsked{}, err
	}
	if text, err = taskText(text); err != nil {
		return mcpAsked{}, err
	}
	client, _, err := a2aClient(ctx, base, token)
	if err != nil {
		return mcpAsked{}, err
	}
	task, contextID, _, err := m.newOutTask(ctx, self, local, contextID, outRow{node: a2aNode, text: text, remoteURL: base, remoteAuth: token})
	if err != nil {
		return mcpAsked{}, err
	}
	out, err := m.sendToAgent(ctx, client, task, contextID, "", text)
	if err != nil {
		_, _ = m.gateway.auth.store.DB.ExecContext(context.WithoutCancel(ctx), "DELETE FROM agent_tasks WHERE id=? AND role='out'", task)
	}
	return out, err
}

// sendToAgent sends one message of our task (remote is the agent's task ID once it has one) and records what came back.
func (m *GatewayMCP) sendToAgent(ctx context.Context, client *a2aclient.Client, task, contextID, remote, text string) (mcpAsked, error) {
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	message.TaskID = a2a.TaskID(remote)
	if contextID != task {
		message.ContextID = contextID // ours until the agent named one
	}
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: message, Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
	if err != nil {
		return mcpAsked{}, err
	}
	out := mcpAsked{TaskID: task, ContextID: contextID}
	switch result := result.(type) {
	case *a2a.Message:
		out.State, out.Answer = taskCompleted, messageText(result)
		out.ContextID = cmpOr(result.ContextID, contextID)
	case *a2a.Task:
		remote = string(result.ID)
		out.State, out.ContextID = fromA2AState(result.Status.State), cmpOr(result.ContextID, contextID)
		if !out.State.open() {
			out.Answer = taskAnswer(result)
		}
	default:
		return out, a2a.ErrInvalidAgentResponse
	}
	// An answer that came at once is the tool's result; it does not also wake the chat.
	_, err = m.gateway.auth.store.DB.ExecContext(ctx, "UPDATE agent_tasks SET to_session=?,context_id=?,state=?,result=?,updated_at=? WHERE id=? AND role='out'",
		remote, out.ContextID, out.State, out.Answer, time.Now().UnixNano(), task)
	return out, err
}

// remoteAnswer answers an A2A agent's question; sendToAgent records the task's new state.
func (m *GatewayMCP) remoteAnswer(ctx context.Context, task string, row outTask, text string) (mcpAsked, error) {
	client, _, err := a2aClient(ctx, row.remoteURL, row.remoteAuth)
	if err != nil {
		return mcpAsked{}, err
	}
	return m.sendToAgent(ctx, client, task, row.context, row.to, text)
}

func (m *GatewayMCP) remoteCancel(ctx context.Context, row outTask) error {
	if row.to == "" {
		return nil // never reached the agent
	}
	client, _, err := a2aClient(ctx, row.remoteURL, row.remoteAuth)
	if err != nil {
		return err
	}
	_, err = client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: a2a.TaskID(row.to)})
	if errors.Is(err, a2a.ErrTaskNotCancelable) || errors.Is(err, a2a.ErrTaskNotFound) {
		return nil
	}
	return err
}

// pollRemoteTasks reads the A2A agents' open tasks and wakes the calling chat with each answer.
func (g *Gateway) pollRemoteTasks(ctx context.Context) error {
	rows, err := g.auth.store.DB.QueryContext(ctx, "SELECT id,from_session,to_session,remote_url,remote_auth,state,updated_at FROM agent_tasks WHERE role='out' AND to_node=? AND to_session<>'' AND state IN ('submitted','working')", a2aNode)
	if err != nil {
		return err
	}
	type open struct {
		id, chat, remote, base, token string
		state                         taskState
		updated                       int64
	}
	var tasks []open
	for rows.Next() {
		var task open
		if err := rows.Scan(&task.id, &task.chat, &task.remote, &task.base, &task.token, &task.state, &task.updated); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, task)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if g.agentTaskPolled == nil {
		g.agentTaskPolled = map[string]time.Time{}
	}
	var failures []error
	for _, task := range tasks {
		if time.Since(g.agentTaskPolled[task.id]) < a2aPollInterval {
			continue
		}
		g.agentTaskPolled[task.id] = time.Now()
		failures = append(failures, g.pollRemoteTask(ctx, task.id, task.chat, task.remote, task.base, task.token, task.state, task.updated))
	}
	for id, at := range g.agentTaskPolled {
		if time.Since(at) > 2*a2aPollInterval {
			delete(g.agentTaskPolled, id)
		}
	}
	return errors.Join(failures...)
}

func (g *Gateway) pollRemoteTask(ctx context.Context, id, chat, remote, base, token string, state taskState, updated int64) error {
	ctx, cancel := context.WithTimeout(ctx, a2aCallTimeout)
	defer cancel()
	client, card, err := a2aClient(ctx, base, token)
	var task *a2a.Task
	if err == nil {
		task, err = client.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(remote)})
	}
	next, answer := state, ""
	switch {
	case errors.Is(err, a2a.ErrTaskNotFound):
		next, answer = taskFailed, "The agent no longer knows this task."
	case err != nil && time.Since(time.Unix(0, updated)) > agentTaskDeliveryWindow:
		next, answer = taskFailed, "The agent did not answer for a day."
	case err != nil:
		return fmt.Errorf("A2A task %s: %w", id, err)
	default:
		next = fromA2AState(task.Status.State)
		answer = cmpOr(taskAnswer(task), "The agent sent no text.")
	}
	db := g.auth.store.DB
	if next.open() {
		if next != state {
			_, err = db.ExecContext(ctx, "UPDATE agent_tasks SET state=?,updated_at=? WHERE id=? AND role='out'", next, time.Now().UnixNano(), id)
		}
		return err
	}
	// The answer reaches the chat like a chat's reply: through the message route, which records it and wakes the chat.
	info, err := storedSession(ctx, g.auth.store.Reader, chat)
	if err != nil || info.StartedBy == nil {
		_, err = db.ExecContext(ctx, "UPDATE agent_tasks SET state=?,result=?,updated_at=? WHERE id=? AND role='out'", next, answer, time.Now().UnixNano(), id)
		return err
	}
	name := base
	if card != nil {
		name = card.Name
	}
	actor := *info.StartedBy
	actor.Agent, actor.chat = info.Harness, info.ID
	return g.mcp.deliver(withActor(ctx, actor), "", chat, agentMessage{Task: id, Kind: "reply", FromSession: remote, FromNode: a2aNode, FromName: name, FromAgent: a2aNode,
		State: next, Text: answer, Operation: id + "-r-" + strconv.FormatInt(updated, 10)}, 0)
}

func (m *GatewayMCP) a2aTools(server *mcp.Server) {
	addTool(server, "call_agent", "Send a message to an A2A agent, given its base URL (whose /.well-known/agent-card.json is read) or its agent card URL. An answer that comes at once is returned; otherwise it arrives in this chat later as a message that wakes it. Use reply to answer its questions and cancel_task to stop it.", mutating, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		AgentURL  string `json:"agent_url" jsonschema:"The agent's http(s) base URL, or its agent card URL."`
		Text      string `json:"text" jsonschema:"The whole request."`
		ContextID string `json:"context_id,omitempty" jsonschema:"An earlier task's context_id to continue that conversation."`
		Token     string `json:"token,omitempty" jsonschema:"Bearer token the agent requires; kept on this machine with the task."`
	}) (*mcp.CallToolResult, mcpAsked, error) {
		out, err := m.callAgent(ctx, in.AgentURL, in.Text, in.ContextID, in.Token)
		return nil, out, err
	})
}
