package network

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
)

// A prompt sent while a turn runs waits in worker memory. Its queued_prompt and dequeued_prompt
// log entries are what every viewer replays, and what a restarted worker rebuilds the queue from.
type QueuedPrompt struct {
	ID    string
	Input acp.PromptInput
	Steer bool
}

// turnRunner decides what the agent does next: the queue, the goal, network retries, steering
// and delivery during a turn. It acts on derived state only after the sink stored it (see
// ConversationSession.applyLogEntry), like an outbox.
//
// Lock order: queueMu, then the core's mu. queueMu orders queue changes and goal publishes with
// their log entries, so it is never taken inside a log callback. Every other field is guarded by
// the core's mu, because the log callback reads them.
type turnRunner struct {
	queueMu       sync.Mutex
	Queue         []QueuedPrompt
	SeenPrompts   map[string]struct{}
	starting      bool
	running       bool // a turn loop owns the agent
	queuesPrompts bool // the agent takes a prompt in while its turn runs (harness.ACPLaunch.QueuesPrompts)
	delivering    int  // prompts the agent has not answered yet
	// steer joins the running turn (harness.ACPLaunch.Steers); nil when steering must cancel.
	steer          func(context.Context, acp.PromptInput) (bool, error)
	delivered      chan struct{}
	userCancelled  bool // the user stopped the running turn
	archived       bool
	now            func() time.Time
	after          func(time.Duration, func()) // schedules network retries; tests replace it
	networkRetries int                         // retries since the last turn that reached the agent's service
	turnsEnded     int                         // a retry is dropped when another turn ended meanwhile
	goal           *session.Goal
	goalCleared    *session.Goal // the last goal, kept to log its "cleared" state
	goalPublished  string
	goalDirty      bool // the agent reported a native goal snapshot
	goalSettled    bool // the native goal was marked met because the agent went idle
	goalPauseDue   bool // the user stopped unprompted native goal work; tell the agent to pause
	nativeGoal     bool // the agent advertises a /goal command
	turnText       []byte
	turnTools      int
	goalBlocked    int
	goalIdle       int
	usageUsed      int64
	usageCost      float64
	// An unprompted cycle (a native goal, a Stop-hook continuation) is closed with a synthetic
	// turn_end once its last usage_update stays quiet for unpromptedUsageSettle.
	unpromptedUsageSettle  time.Duration
	inPromptTurn           bool
	lastParentKind         string
	unpromptedUsagePending bool
	settleGen              uint64
	settleTimer            *time.Timer
}

var (
	errNotQueued   = errors.New("prompt is not queued")
	ErrEmptyPrompt = errors.New("text or image is required")
)

// The lock covers the only time the gateway and worker can both own the conversation log.
func LockPromptHandoff(ctx context.Context, directory string) (*flock.Flock, error) {
	lock := flock.New(filepath.Join(directory, "prompt.lock"))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("prompt handoff lock unavailable")
	}
	return lock, nil
}

// A long transcript can push a still-queued prompt out of the worker's bounded replay tail.
func restorePendingQueue(ctx context.Context, db *sql.DB, id string, conversation *ConversationSession) error {
	if !conversation.Log.Truncated() {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT queued.body FROM conversation_events queued
		WHERE queued.session_id=? AND queued.kind='queued_prompt'
		AND NOT EXISTS (
			SELECT 1 FROM conversation_events finished WHERE finished.session_id=queued.session_id
			AND finished.kind IN ('prompt','dequeued_prompt')
			AND json_extract(finished.body, '$.payload.queueId')=json_extract(queued.body, '$.payload.queueId')
			AND finished.seq>queued.seq
		) ORDER BY queued.seq`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	conversation.Queue = nil
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return err
		}
		conversation.ReplayEntry(entry)
	}
	return rows.Err()
}

func (cs *ConversationSession) clock() time.Time {
	if cs.now != nil {
		return cs.now()
	}
	return time.Now()
}

// closedErr reports why no prompt can be sent. The caller holds mu.
func (cs *ConversationSession) closedErr() error {
	switch {
	case cs.archived:
		return session.ErrSessionArchived
	case cs.agentExited:
		return ErrSessionEnded
	}
	return nil
}

// Submit sends input now, or queues it behind the running turn and returns its queue ID.
// A steer goes to the front of the queue and interrupts the running turn. When the prompt
// starts immediately, first receives the result of its turn.
func (cs *ConversationSession) Submit(ctx context.Context, input acp.PromptInput, steer bool) (queueID string, first <-chan error, err error) {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	idempotent := input.QueueID != ""
	if idempotent {
		if _, seen := cs.SeenPrompts[input.QueueID]; seen {
			return input.QueueID, nil, nil
		}
		if stored, err := cs.promptStored(ctx, input.QueueID); stored || err != nil {
			return input.QueueID, nil, err
		}
	}
	cs.mu.Lock()
	if err := cs.closedErr(); err != nil {
		cs.mu.Unlock()
		return "", nil, err
	}
	if !idempotent && !cs.starting && !cs.running && len(cs.Queue) == 0 {
		cs.running, cs.userCancelled = true, false
		cs.mu.Unlock()
		result := make(chan error, 1)
		ctx = context.WithoutCancel(ctx)
		obs.Go("acp.turns", func() { cs.runTurns(ctx, &input, result) })
		return "", result, nil
	}
	item := QueuedPrompt{ID: input.QueueID, Input: input, Steer: steer}
	if item.ID == "" {
		item.ID = rand.Text()
	}
	// The prompt entry that sends it carries the ID, which is what takes it off every viewer's queue.
	item.Input.QueueID = item.ID
	cs.mu.Unlock()
	cs.Log.Append("queued_prompt", cs.clock().UnixMilli(), QueuedPromptPayload(item))
	if idempotent {
		if err := cs.Log.Flush(); err != nil {
			return "", nil, err
		}
		cs.SeenPrompts[item.ID] = struct{}{}
	}
	cs.mu.Lock()
	cs.Queue = placeQueued(cs.Queue, item)
	running := cs.running
	cs.mu.Unlock()
	cs.afterQueued(ctx, item.Steer, running)
	return item.ID, nil, nil
}

// afterQueued starts the queue, hands an agent message to a running turn, or steers the current
// turn. An agent that serves _session/steering takes a steer into its turn; any other is
// interrupted with session/cancel first, which the UI warns about. The caller holds queueMu.
func (cs *ConversationSession) afterQueued(ctx context.Context, steer, running bool) {
	if !running {
		cs.maybeDrain()
		return
	}
	if !steer {
		cs.deliverDuringTurnLocked(false)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cs.mu.Lock()
	broker, steerTurn := cs.broker, cs.steer
	var item QueuedPrompt
	if len(cs.Queue) > 0 {
		item = cs.Queue[0]
	}
	cs.mu.Unlock()
	if steerTurn != nil && item.Steer {
		cs.steerInto(ctx, steerTurn, item)
		return
	}
	cs.Log.Append("notice", cs.clock().UnixMilli(), MustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "info", "title": "Interrupted to steer",
	}))
	if err := broker.Cancel(ctx); err != nil {
		_, event := obs.Start(ctx, "acp.turn", "steer_cancel")
		event.Set("session_id", cs.sessionID)
		event.Fail(err)
		event.End()
	}
}

// steerInto hands a steered prompt to the running turn. While queueMu is held no turn loop can
// send it too; when no turn takes it, it stays first in the queue for the next turn.
func (cs *ConversationSession) steerInto(ctx context.Context, steerTurn func(context.Context, acp.PromptInput) (bool, error), item QueuedPrompt) {
	cs.writes.RLock()
	injected, err := steerTurn(ctx, item.Input)
	cs.writes.RUnlock()
	if err != nil {
		_, event := obs.Start(ctx, "acp.turn", "steer")
		event.Set("session_id", cs.sessionID)
		event.Fail(err)
		event.End()
		cs.Log.Append("notice", cs.clock().UnixMilli(), MustRawJSON(map[string]string{
			"sessionUpdate": "notice", "severity": "warning", "title": "The message goes in when this turn ends", "description": err.Error(),
		}))
		return
	}
	if injected {
		cs.mu.Lock()
		cs.Queue = slices.DeleteFunc(cs.Queue, func(queued QueuedPrompt) bool { return queued.ID == item.ID })
		cs.mu.Unlock()
	}
}

// placeQueued adds or replaces item. A newly steered prompt moves to the front; an edit keeps its place.
func placeQueued(queue []QueuedPrompt, item QueuedPrompt) []QueuedPrompt {
	index := slices.IndexFunc(queue, func(queued QueuedPrompt) bool { return queued.ID == item.ID })
	if index >= 0 && (!item.Steer || queue[index].Steer) {
		queue[index] = item
		return queue
	}
	if index >= 0 {
		queue = slices.Delete(queue, index, index+1)
	}
	if item.Steer {
		return slices.Insert(queue, 0, item)
	}
	return append(queue, item)
}

func (cs *ConversationSession) EditQueued(id, text string) error {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	cs.mu.Lock()
	index := slices.IndexFunc(cs.Queue, func(queued QueuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		cs.mu.Unlock()
		return errNotQueued
	}
	item := cs.Queue[index]
	item.Input.Text = text
	if text == "" && item.Input.Image == nil && len(item.Input.Images) == 0 && item.Input.ResourceLink == nil {
		cs.mu.Unlock()
		return ErrEmptyPrompt
	}
	cs.Queue[index] = item
	cs.mu.Unlock()
	cs.Log.Append("queued_prompt", cs.clock().UnixMilli(), QueuedPromptPayload(item))
	return nil
}

func (cs *ConversationSession) RemoveQueued(id string) error {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	cs.mu.Lock()
	index := slices.IndexFunc(cs.Queue, func(queued QueuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		cs.mu.Unlock()
		return errNotQueued
	}
	cs.Queue = slices.Delete(cs.Queue, index, index+1)
	cs.mu.Unlock()
	cs.logDequeued(id, "removed")
	return nil
}

// SteerQueued moves a queued prompt to the front and interrupts the running turn so it goes next.
func (cs *ConversationSession) SteerQueued(ctx context.Context, id string) error {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	cs.mu.Lock()
	index := slices.IndexFunc(cs.Queue, func(queued QueuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		cs.mu.Unlock()
		return errNotQueued
	}
	item := cs.Queue[index]
	item.Steer = true
	cs.Queue = placeQueued(cs.Queue, item)
	running := cs.running
	cs.mu.Unlock()
	cs.Log.Append("queued_prompt", cs.clock().UnixMilli(), QueuedPromptPayload(item))
	cs.afterQueued(ctx, item.Steer, running)
	return nil
}

// deliverDuringTurn hands queued prompts to an agent that takes them in while its turn runs
// (QueuesPrompts): agent messages at once, and the next user prompt when a model cycle ends, so
// neither waits for a long turn such as a native /goal. User prompts stay editable until then.
// Other agents get both from runTurns when the turn ends.
func (cs *ConversationSession) deliverDuringTurn(cycleEnd bool) {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	cs.deliverDuringTurnLocked(cycleEnd)
}

// deliverDuringTurnLocked is deliverDuringTurn with queueMu held.
func (cs *ConversationSession) deliverDuringTurnLocked(cycleEnd bool) {
	cs.mu.Lock()
	// An open permission request does not hold them: the agent queues them behind its answer.
	if !cs.queuesPrompts || cs.delivering == 0 || cs.closedErr() != nil {
		cs.mu.Unlock()
		return
	}
	var send []QueuedPrompt
	cs.Queue = slices.DeleteFunc(cs.Queue, func(item QueuedPrompt) bool {
		take := !item.Steer && (item.Input.From != nil || cycleEnd)
		if take {
			cycleEnd = cycleEnd && item.Input.From != nil // one user prompt per cycle
			send = append(send, item)
		}
		return take
	})
	broker := cs.broker
	cs.delivering += len(send)
	cs.mu.Unlock()
	for _, item := range send {
		sent, answered := make(chan struct{}), make(chan struct{})
		ctx := acp.WithAccepted(context.Background(), sync.OnceFunc(func() { close(sent) }))
		obs.Go("acp.deliver", func() {
			defer close(answered)
			cs.writes.RLock()
			defer cs.writes.RUnlock()
			if err := cs.deliver(ctx, broker, item.Input); err != nil {
				select {
				case <-sent:
				default:
					cs.dropUnsent(item.ID, err)
				}
			}
		})
		// The next one waits until this one is logged and on its way, so the agent gets them in order.
		select {
		case <-sent:
		case <-answered:
		}
	}
}

// dropUnsent records a queued prompt the agent never received, so it does not vanish unexplained.
func (cs *ConversationSession) dropUnsent(id string, err error) {
	cs.Log.Append("notice", cs.clock().UnixMilli(), MustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "error", "title": "The agent could not run this prompt", "description": err.Error(),
	}))
	cs.logDequeued(id, "failed")
}

func (cs *ConversationSession) logDequeued(id, reason string) {
	cs.Log.Append("dequeued_prompt", cs.clock().UnixMilli(), MustRawJSON(map[string]string{"queueId": id, "reason": reason}))
}

// runTurns sends input, then keeps sending queued prompts and goal continuations until none is due.
func (cs *ConversationSession) runTurns(ctx context.Context, input *acp.PromptInput, first chan<- error) {
	for {
		if input == nil {
			next, ok := cs.nextPrompt()
			if !ok {
				return
			}
			input = &next
		}
		accepted, report := false, first
		// The HTTP request waits only until the prompt is logged; later failures land in the event log.
		turnCtx := acp.WithAccepted(ctx, func() {
			if !accepted && report != nil {
				report <- nil
			}
			accepted = true
		})
		err := cs.Prompt(turnCtx, *input)
		cs.awaitDelivered()
		if err != nil && !accepted && input.QueueID != "" {
			cs.dropUnsent(input.QueueID, err)
		}
		if first != nil {
			if !accepted {
				first <- err
			}
			first = nil
		}
		cs.finishTurn(ctx, err)
		cs.retryAfterNetworkDrop(err)
		input = nil
	}
}

// nextPrompt picks what to send after a turn, or releases the agent. It never sends while the
// user must answer a request, after the agent exited, or once the chat is archived.
func (cs *ConversationSession) nextPrompt() (acp.PromptInput, bool) {
	cs.queueMu.Lock()
	defer cs.queueMu.Unlock()
	cs.mu.Lock()
	if cs.closedErr() != nil || cs.state == "needs_you" || len(cs.Queue) == 0 && !cs.goalWantsTurn() {
		cs.running = false
		cs.mu.Unlock()
		return acp.PromptInput{}, false
	}
	cs.userCancelled = false
	if len(cs.Queue) == 0 {
		prompt := goalContinuation(cs.goal)
		cs.mu.Unlock()
		return acp.PromptInput{Text: prompt}, true
	}
	item := cs.Queue[0]
	cs.Queue = cs.Queue[1:]
	cs.mu.Unlock()
	return item.Input, true
}

// maybeDrain starts the turn loop when the agent is free and a queued prompt or goal turn is due.
func (cs *ConversationSession) maybeDrain() {
	cs.mu.Lock()
	start := !cs.starting && !cs.running && cs.closedErr() == nil && cs.state != "needs_you" &&
		(len(cs.Queue) > 0 || cs.goalWantsTurn())
	if start {
		cs.running = true
	}
	cs.mu.Unlock()
	if start {
		obs.Go("acp.turns", func() { cs.runTurns(context.Background(), nil, nil) })
	}
}

// ResumeTurns delivers prompts a restarted worker found queued, and continues an active goal.
func (cs *ConversationSession) ResumeTurns() {
	cs.mu.Lock()
	cs.starting = false
	cs.mu.Unlock()
	cs.maybeDrain()
}

func QueuedPromptPayload(item QueuedPrompt) json.RawMessage {
	var body map[string]any
	_ = json.Unmarshal(PromptLogPayload(item.Input), &body)
	body["queueId"] = item.ID
	if item.Steer {
		body["steer"] = true
	}
	return MustRawJSON(body)
}

// PromptInputFromLog reverses promptLogPayload so a restarted worker can still send a queued prompt.
func PromptInputFromLog(payload json.RawMessage) (acp.PromptInput, error) {
	var body struct {
		QueueID string `json:"queueId"`
		Text    *struct {
			Text string `json:"text"`
		} `json:"text"`
		Image *struct {
			Blob          string `json:"blob"`
			Width, Height int
		} `json:"image"`
		Images []struct {
			Blob, Name, Kind string
			Width, Height    int
		} `json:"images"`
		ResourceLink *struct {
			Blob, Name string
		} `json:"resource_link"`
		From *acp.PromptFrom `json:"from"`
	}
	var input acp.PromptInput
	if err := json.Unmarshal(payload, &body); err != nil {
		return input, err
	}
	input.QueueID, input.From = body.QueueID, body.From
	sha := func(blob string) string { return strings.TrimPrefix(blob, "sha256:") }
	if body.Text != nil {
		input.Text = body.Text.Text
	}
	if body.Image != nil {
		input.Image = &acp.PromptImage{Sha: sha(body.Image.Blob), Width: body.Image.Width, Height: body.Image.Height}
	}
	for _, image := range body.Images {
		input.Images = append(input.Images, acp.PromptImage{Sha: sha(image.Blob), Name: image.Name, Width: image.Width, Height: image.Height})
	}
	if body.ResourceLink != nil {
		input.ResourceLink = &acp.PromptResourceLink{Sha: sha(body.ResourceLink.Blob), Name: body.ResourceLink.Name}
	}
	if input.Text == "" && input.Image == nil && len(input.Images) == 0 && input.ResourceLink == nil {
		return input, ErrEmptyPrompt
	}
	return input, nil
}

// ReplayEntry rebuilds the queue and goal from history while a worker starts; live changes are
// made in memory first, so they never read their own entries back.
func (cs *ConversationSession) ReplayEntry(entry acp.LogEntry) {
	switch entry.Kind {
	case "queued_prompt":
		var meta struct {
			QueueID string `json:"queueId"`
			Steer   bool   `json:"steer"`
		}
		if json.Unmarshal(entry.Payload, &meta) != nil || meta.QueueID == "" {
			return
		}
		input, err := PromptInputFromLog(entry.Payload)
		if err != nil {
			return
		}
		cs.SeenPrompts[meta.QueueID] = struct{}{}
		cs.Queue = placeQueued(cs.Queue, QueuedPrompt{ID: meta.QueueID, Input: input, Steer: meta.Steer})
	case "prompt", "dequeued_prompt":
		var meta struct {
			QueueID string `json:"queueId"`
		}
		if json.Unmarshal(entry.Payload, &meta) == nil && meta.QueueID != "" {
			cs.SeenPrompts[meta.QueueID] = struct{}{}
			cs.Queue = slices.DeleteFunc(cs.Queue, func(queued QueuedPrompt) bool { return queued.ID == meta.QueueID })
		}
	case "goal_update":
		var goal session.Goal
		if json.Unmarshal(entry.Payload, &goal) != nil {
			return
		}
		cs.goalPublished = string(entry.Payload)
		if goal.State == "cleared" {
			cs.goal, cs.goalCleared = nil, &goal
		} else {
			cs.goal = &goal
		}
	}
}
