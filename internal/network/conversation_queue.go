package network

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
)

// A prompt sent while a turn runs waits in worker memory. Its queued_prompt and dequeued_prompt
// log entries are what every viewer replays, and what a restarted worker rebuilds the queue from.
type queuedPrompt struct {
	ID    string
	Input acp.PromptInput
	Steer bool
}

var (
	errNotQueued   = errors.New("prompt is not queued")
	errEmptyPrompt = errors.New("text or image is required")
)

func (session *ConversationSession) clock() time.Time {
	if session.now != nil {
		return session.now()
	}
	return time.Now()
}

// closedErr reports why no prompt can be sent. The caller holds mu.
func (session *ConversationSession) closedErr() error {
	switch {
	case session.archived:
		return errSessionArchived
	case session.agentExited:
		return ErrSessionEnded
	}
	return nil
}

// Submit sends input now, or queues it behind the running turn and returns its queue ID.
// A steer goes to the front of the queue and interrupts the running turn. When the prompt
// starts immediately, first receives the result of its turn.
func (session *ConversationSession) Submit(ctx context.Context, input acp.PromptInput, steer bool) (queueID string, first <-chan error, err error) {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.mu.Lock()
	if err := session.closedErr(); err != nil {
		session.mu.Unlock()
		return "", nil, err
	}
	if !session.running && len(session.queue) == 0 {
		session.running, session.userCancelled = true, false
		session.mu.Unlock()
		result := make(chan error, 1)
		ctx = context.WithoutCancel(ctx)
		obs.Go("acp.turns", func() { session.runTurns(ctx, &input, result) })
		return "", result, nil
	}
	item := queuedPrompt{ID: rand.Text(), Input: input, Steer: steer}
	session.queue = placeQueued(session.queue, item)
	running := session.running
	session.mu.Unlock()
	session.afterQueued(ctx, item, running)
	return item.ID, nil, nil
}

// afterQueued records a queue change and interrupts the turn for a steer. The caller holds queueMu.
func (session *ConversationSession) afterQueued(ctx context.Context, item queuedPrompt, running bool) {
	session.log.Append("queued_prompt", session.clock().UnixMilli(), queuedPromptPayload(item))
	if !running {
		session.maybeDrain()
		return
	}
	if !item.Steer {
		return
	}
	session.log.Append("notice", session.clock().UnixMilli(), mustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "info", "title": "Interrupted to steer",
	}))
	session.mu.Lock()
	broker := session.broker
	session.mu.Unlock()
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := broker.Cancel(cancelCtx); err != nil {
		_, event := obs.Start(ctx, "acp.turn", "steer_cancel")
		event.Set("session_id", session.sessionID)
		event.Fail(err)
		event.End()
	}
}

// placeQueued adds or replaces item. A newly steered prompt moves to the front; an edit keeps its place.
func placeQueued(queue []queuedPrompt, item queuedPrompt) []queuedPrompt {
	index := slices.IndexFunc(queue, func(queued queuedPrompt) bool { return queued.ID == item.ID })
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

func (session *ConversationSession) EditQueued(id, text string) error {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.mu.Lock()
	index := slices.IndexFunc(session.queue, func(queued queuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		session.mu.Unlock()
		return errNotQueued
	}
	item := session.queue[index]
	item.Input.Text = text
	if text == "" && item.Input.Image == nil && len(item.Input.Images) == 0 && item.Input.ResourceLink == nil {
		session.mu.Unlock()
		return errEmptyPrompt
	}
	session.queue[index] = item
	session.mu.Unlock()
	session.log.Append("queued_prompt", session.clock().UnixMilli(), queuedPromptPayload(item))
	return nil
}

func (session *ConversationSession) RemoveQueued(id string) error {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.mu.Lock()
	index := slices.IndexFunc(session.queue, func(queued queuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		session.mu.Unlock()
		return errNotQueued
	}
	session.queue = slices.Delete(session.queue, index, index+1)
	session.mu.Unlock()
	session.logDequeued(id, "removed")
	return nil
}

// SteerQueued moves a queued prompt to the front and interrupts the running turn so it goes next.
func (session *ConversationSession) SteerQueued(ctx context.Context, id string) error {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.mu.Lock()
	index := slices.IndexFunc(session.queue, func(queued queuedPrompt) bool { return queued.ID == id })
	if index < 0 {
		session.mu.Unlock()
		return errNotQueued
	}
	item := session.queue[index]
	item.Steer = true
	session.queue = placeQueued(session.queue, item)
	running := session.running
	session.mu.Unlock()
	session.afterQueued(ctx, item, running)
	return nil
}

func (session *ConversationSession) logDequeued(id, reason string) {
	session.log.Append("dequeued_prompt", session.clock().UnixMilli(), mustRawJSON(map[string]string{"queueId": id, "reason": reason}))
}

// runTurns sends input, then keeps sending queued prompts and goal continuations until none is due.
func (session *ConversationSession) runTurns(ctx context.Context, input *acp.PromptInput, first chan<- error) {
	for {
		if input == nil {
			next, ok := session.nextPrompt()
			if !ok {
				return
			}
			input = &next
		}
		turnCtx, accepted := ctx, false
		if first != nil {
			// The HTTP request waits only until the prompt is logged; later failures land in the event log.
			report := first
			turnCtx = acp.WithAccepted(ctx, func() {
				if !accepted {
					accepted = true
					report <- nil
				}
			})
		}
		err := session.Prompt(turnCtx, *input)
		if first != nil {
			if !accepted {
				first <- err
			}
			first = nil
		}
		session.finishTurn(ctx, err)
		input = nil
	}
}

// nextPrompt picks what to send after a turn, or releases the agent. It never sends while the
// user must answer a request, after the agent exited, or once the chat is archived.
func (session *ConversationSession) nextPrompt() (acp.PromptInput, bool) {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.mu.Lock()
	if session.closedErr() != nil || session.state == "needs_you" || len(session.queue) == 0 && !session.goalWantsTurn() {
		session.running = false
		session.mu.Unlock()
		return acp.PromptInput{}, false
	}
	session.userCancelled = false
	if len(session.queue) == 0 {
		prompt := goalContinuation(session.goal)
		session.mu.Unlock()
		return acp.PromptInput{Text: prompt}, true
	}
	item := session.queue[0]
	session.queue = session.queue[1:]
	session.mu.Unlock()
	session.logDequeued(item.ID, "sent")
	return item.Input, true
}

// maybeDrain starts the turn loop when the agent is free and a queued prompt or goal turn is due.
func (session *ConversationSession) maybeDrain() {
	session.mu.Lock()
	start := !session.running && session.closedErr() == nil && session.state != "needs_you" &&
		(len(session.queue) > 0 || session.goalWantsTurn())
	if start {
		session.running = true
	}
	session.mu.Unlock()
	if start {
		obs.Go("acp.turns", func() { session.runTurns(context.Background(), nil, nil) })
	}
}

// ResumeTurns delivers prompts a restarted worker found queued, and continues an active goal.
func (session *ConversationSession) ResumeTurns() { session.maybeDrain() }

func queuedPromptPayload(item queuedPrompt) json.RawMessage {
	var body map[string]any
	_ = json.Unmarshal(promptLogPayload(item.Input), &body)
	body["queueId"] = item.ID
	if item.Steer {
		body["steer"] = true
	}
	return mustRawJSON(body)
}

// promptInputFromLog reverses promptLogPayload so a restarted worker can still send a queued prompt.
func promptInputFromLog(payload json.RawMessage) (acp.PromptInput, error) {
	var body struct {
		Text *struct {
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
	}
	var input acp.PromptInput
	if err := json.Unmarshal(payload, &body); err != nil {
		return input, err
	}
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
		return input, errEmptyPrompt
	}
	return input, nil
}

// replayEntry rebuilds the queue and goal from history while a worker starts; live changes are
// made in memory first, so they never read their own entries back.
func (session *ConversationSession) replayEntry(entry acp.LogEntry) {
	switch entry.Kind {
	case "queued_prompt":
		var meta struct {
			QueueID string `json:"queueId"`
			Steer   bool   `json:"steer"`
		}
		if json.Unmarshal(entry.Payload, &meta) != nil || meta.QueueID == "" {
			return
		}
		input, err := promptInputFromLog(entry.Payload)
		if err != nil {
			return
		}
		session.queue = placeQueued(session.queue, queuedPrompt{ID: meta.QueueID, Input: input, Steer: meta.Steer})
	case "dequeued_prompt":
		var meta struct {
			QueueID string `json:"queueId"`
		}
		if json.Unmarshal(entry.Payload, &meta) == nil {
			session.queue = slices.DeleteFunc(session.queue, func(queued queuedPrompt) bool { return queued.ID == meta.QueueID })
		}
	case "goal_update":
		var goal Goal
		if json.Unmarshal(entry.Payload, &goal) != nil {
			return
		}
		session.goalPublished = string(entry.Payload)
		if goal.State == "cleared" {
			session.goal, session.goalCleared = nil, &goal
		} else {
			session.goal = &goal
		}
	}
}
