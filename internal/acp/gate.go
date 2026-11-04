package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"

	sdkacp "github.com/coder/acp-go-sdk"
)

// The SDK queues 1024 notifications and closes the connection on overflow, and its reader never
// waits for handlers. The gate stops reading the agent once this many notifications await a handler,
// so a flood of tiny chunks slows the agent's pipe instead of dropping the connection.
const maxInFlightNotifications = 512

type notificationGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	inflight int
	r        *bufio.Reader
	pending  []byte
}

func newNotificationGate(r io.Reader) *notificationGate {
	g := &notificationGate{r: bufio.NewReader(r)}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Read serves whole JSON-RPC lines; shortcut: one line is held in memory, bounded only by the SDK's own frame limit.
func (g *notificationGate) Read(p []byte) (int, error) {
	if len(g.pending) == 0 {
		line, err := g.r.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		if countsAsHandled(line) {
			g.mu.Lock()
			for g.inflight >= maxInFlightNotifications {
				g.cond.Wait()
			}
			g.inflight++
			g.mu.Unlock()
		}
		g.pending = line
	}
	n := copy(p, g.pending)
	g.pending = g.pending[n:]
	return n, nil
}

func (g *notificationGate) release() {
	g.mu.Lock()
	g.inflight--
	if g.inflight <= maxInFlightNotifications/2 { // wake in batches, not on every chunk
		g.cond.Signal()
	}
	g.mu.Unlock()
}

// countsAsHandled mirrors the SDK's dispatch: only session/update notifications that will reach SessionUpdate
// are counted, because its return releases the slot. Extension notifications are rare and not gated.
func countsAsHandled(line []byte) bool {
	var message struct {
		ID     json.RawMessage            `json:"id"`
		Method string                     `json:"method"`
		Params sdkacp.SessionNotification `json:"params"` // one typed pass: a params error means the SDK would not call the handler either
	}
	return json.Unmarshal(line, &message) == nil && len(message.ID) == 0 &&
		message.Method == sdkacp.ClientMethodSessionUpdate && message.Params.Validate() == nil
}

type gatedClient struct {
	sdkacp.Client
	gate *notificationGate
}

func (c gatedClient) SessionUpdate(ctx context.Context, n sdkacp.SessionNotification) error {
	defer c.gate.release()
	return c.Client.SessionUpdate(ctx, n)
}

func (c gatedClient) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	h, ok := c.Client.(sdkacp.ExtensionMethodHandler)
	if !ok {
		return nil, sdkacp.NewMethodNotFound(method)
	}
	return h.HandleExtensionMethod(ctx, method, params)
}

// The SDK finds these through type assertions, so embedding the interface hides them; forward each.
func (c gatedClient) UnstableCreateElicitation(ctx context.Context, p sdkacp.UnstableCreateElicitationRequest) (sdkacp.UnstableCreateElicitationResponse, error) {
	h, ok := c.Client.(interface {
		UnstableCreateElicitation(context.Context, sdkacp.UnstableCreateElicitationRequest) (sdkacp.UnstableCreateElicitationResponse, error)
	})
	if !ok {
		return sdkacp.UnstableCreateElicitationResponse{}, sdkacp.NewMethodNotFound(sdkacp.ClientMethodElicitationCreate)
	}
	return h.UnstableCreateElicitation(ctx, p)
}

func (c gatedClient) UnstableCompleteElicitation(ctx context.Context, p sdkacp.UnstableCompleteElicitationNotification) error {
	h, ok := c.Client.(interface {
		UnstableCompleteElicitation(context.Context, sdkacp.UnstableCompleteElicitationNotification) error
	})
	if !ok {
		return sdkacp.NewMethodNotFound(sdkacp.ClientMethodElicitationComplete)
	}
	return h.UnstableCompleteElicitation(ctx, p)
}

func (c gatedClient) UnstableConnectMcp(ctx context.Context, p sdkacp.UnstableConnectMcpRequest) (sdkacp.UnstableConnectMcpResponse, error) {
	h, ok := c.Client.(interface {
		UnstableConnectMcp(context.Context, sdkacp.UnstableConnectMcpRequest) (sdkacp.UnstableConnectMcpResponse, error)
	})
	if !ok {
		return sdkacp.UnstableConnectMcpResponse{}, sdkacp.NewMethodNotFound(sdkacp.ClientMethodMcpConnect)
	}
	return h.UnstableConnectMcp(ctx, p)
}

func (c gatedClient) UnstableDisconnectMcp(ctx context.Context, p sdkacp.UnstableDisconnectMcpRequest) (sdkacp.UnstableDisconnectMcpResponse, error) {
	h, ok := c.Client.(interface {
		UnstableDisconnectMcp(context.Context, sdkacp.UnstableDisconnectMcpRequest) (sdkacp.UnstableDisconnectMcpResponse, error)
	})
	if !ok {
		return sdkacp.UnstableDisconnectMcpResponse{}, sdkacp.NewMethodNotFound(sdkacp.ClientMethodMcpDisconnect)
	}
	return h.UnstableDisconnectMcp(ctx, p)
}

// NewClientConnection connects client to an agent with backpressure on notification floods.
// Pass the outermost client so its overrides are the handlers that release the gate.
func NewClientConnection(client sdkacp.Client, peerInput io.Writer, peerOutput io.Reader) *sdkacp.ClientSideConnection {
	gate := newNotificationGate(peerOutput)
	return sdkacp.NewClientSideConnection(gatedClient{Client: client, gate: gate}, peerInput, gate)
}
