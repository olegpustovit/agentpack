package acp

import (
	"bufio"
	"bytes"
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
	log      *EventLog // takes the lines the SDK cannot decode, in wire order
}

func newNotificationGate(r io.Reader, log *EventLog) *notificationGate {
	g := &notificationGate{r: bufio.NewReader(r), log: log}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Read serves whole JSON-RPC lines; shortcut: one line is held in memory, bounded only by the SDK's own frame limit.
// It is the single ordered ingestion point: a line logged here waits for the handlers of every
// update read before it, so the log keeps the agent's wire order.
func (g *notificationGate) Read(p []byte) (int, error) {
	for len(g.pending) == 0 {
		line, err := g.r.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		if apply := g.rawLine(line); apply != nil {
			g.drain()
			apply(g.log)
			if err != nil {
				return 0, err
			}
			continue
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

// rawLine is how the log takes a line the SDK must not see, or nil.
func (g *notificationGate) rawLine(line []byte) func(*EventLog) {
	if g.log == nil || !bytes.Contains(line, []byte(`"method"`)) {
		return nil
	}
	if apply := agentLogLine(line); apply != nil {
		return apply
	}
	return rawSessionUpdate(line)
}

// drain waits until every update read so far has been handled.
func (g *notificationGate) drain() {
	g.mu.Lock()
	for g.inflight != 0 {
		g.cond.Wait()
	}
	g.mu.Unlock()
}

func (g *notificationGate) release() {
	g.mu.Lock()
	g.inflight--
	if g.inflight == 0 {
		g.cond.Broadcast()
	} else if g.inflight <= maxInFlightNotifications/2 { // wake in batches, not on every chunk
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
	gate := newNotificationGate(peerOutput, nil)
	return sdkacp.NewClientSideConnection(gatedClient{Client: client, gate: gate}, peerInput, gate)
}
