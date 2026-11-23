package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
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

// The lock covers the only time the gateway and worker can both own the conversation log.
func lockPromptHandoff(ctx context.Context, directory string) (*flock.Flock, error) {
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

func (g *Gateway) servePrompt(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	suffix := "/prompt"
	if strings.HasSuffix(r.URL.Path, "/images") {
		suffix = "/prompt/images"
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		if authorizeACPWrite(w, r, record.Session) {
			proxyWorkerConversation(w, r, record, suffix)
		}
		return
	}
	info, err := storedSession(ctx, g.auth.store.Reader, id)
	if err != nil || info.Transport != "acp" || !validSessionID(id) {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	if !authorizeACPWrite(w, r, info) {
		return
	}
	if info.ConversationState != "starting" || !info.EndedAt.IsZero() {
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	prompt, steer, ok := readPromptRequest(w, r)
	if !ok {
		return
	}
	prompt.From = promptFromContext(ctx)
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		promptQueueError(w, r, err)
		return
	}
	lock, err := lockPromptHandoff(ctx, directory)
	if err != nil {
		promptQueueError(w, r, err)
		return
	}
	if record, err := g.workerRecord(ctx, id); err == nil {
		_ = lock.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if authorizeACPWrite(w, r, record.Session) {
			proxyWorkerConversation(w, r, record, suffix)
		}
		return
	}
	defer lock.Close()
	if prompt.QueueID == "" {
		prompt.QueueID = rand.Text()
	}
	store := g.auth.store
	seen, err := storedPromptID(ctx, store, id, prompt.QueueID)
	if err != nil {
		promptQueueError(w, r, err)
		return
	}
	if !seen {
		item := queuedPrompt{ID: prompt.QueueID, Input: prompt, Steer: steer}
		err := state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
			return appendStartingPrompt(ctx, tx, id, item)
		})
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session unavailable", http.StatusConflict)
			return
		}
		if err != nil {
			promptQueueError(w, r, err)
			return
		}
	}

	writePromptAccepted(w, prompt.QueueID)
}

func appendStartingPrompt(ctx context.Context, tx *sql.Tx, id string, item queuedPrompt) error {
	now := time.Now()
	entry := acp.LogEntry{TS: now.UnixMilli(), Kind: "queued_prompt", Payload: queuedPromptPayload(item)}
	err := tx.QueryRowContext(ctx, `UPDATE sessions SET last_seq=last_seq+1,last_event_at=?,
		title=CASE WHEN title='' THEN ? ELSE title END
		WHERE id=? AND conversation_state='starting' AND ended_at=0 AND archived_at=0 AND node_id=`+state.LocalNodeSQL+` RETURNING last_seq`,
		now.UnixNano(), titleFromPrompt(item.Input.Text), id).Scan(&entry.Seq)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, entry.Seq, string(mustRawJSON(entry)))
	return err
}

// removeStartingPrompt drops a prompt queued while its chat was still starting, as the worker
// would ("removed"), so the worker it starts never sends it. False when there was nothing to drop
// here: the worker took over, or the chat is not starting.
func (g *Gateway) removeStartingPrompt(ctx context.Context, id, queueID string) (bool, error) {
	directory := filepath.Join(g.options.Home, "network", "sessions", id)
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return false, err
	}
	lock, err := lockPromptHandoff(ctx, directory)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if _, err := g.workerRecord(ctx, id); err == nil {
		return false, nil
	}
	err = state.Transaction(ctx, g.auth.store.DB, func(tx *sql.Tx) error {
		now := time.Now()
		entry := acp.LogEntry{TS: now.UnixMilli(), Kind: "dequeued_prompt", Payload: mustRawJSON(map[string]string{"queueId": queueID, "reason": "removed"})}
		err := tx.QueryRowContext(ctx, `UPDATE sessions SET last_seq=last_seq+1,last_event_at=?
			WHERE id=? AND conversation_state='starting' AND ended_at=0 AND archived_at=0 AND node_id=`+state.LocalNodeSQL+` RETURNING last_seq`, now.UnixNano(), id).Scan(&entry.Seq)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, entry.Seq, string(mustRawJSON(entry)))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func promptQueueError(w http.ResponseWriter, r *http.Request, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(err)
	}
	http.Error(w, "could not queue prompt", http.StatusServiceUnavailable)
}

// A long transcript can push a still-queued prompt out of the worker's bounded replay tail.
func restorePendingQueue(ctx context.Context, db *sql.DB, id string, conversation *ConversationSession) error {
	if !conversation.log.Truncated() {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT queued.body FROM conversation_events queued
		WHERE queued.session_id=? AND json_valid(queued.body)
		AND json_extract(queued.body, '$.kind')='queued_prompt'
		AND NOT EXISTS (
			SELECT 1 FROM conversation_events finished WHERE finished.session_id=queued.session_id
			AND json_valid(finished.body)
			AND json_extract(finished.body, '$.kind') IN ('queued_prompt','prompt','dequeued_prompt')
			AND json_extract(finished.body, '$.kind') IN ('prompt','dequeued_prompt')
			AND json_extract(finished.body, '$.payload.queueId')=json_extract(queued.body, '$.payload.queueId')
			AND finished.seq>queued.seq
		) ORDER BY queued.seq`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	conversation.queue = nil
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var entry acp.LogEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return err
		}
		conversation.replayEntry(entry)
	}
	return rows.Err()
}

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
	idempotent := input.QueueID != ""
	if idempotent {
		if _, seen := session.seenPrompts[input.QueueID]; seen {
			return input.QueueID, nil, nil
		}
		if session.promptStored != nil {
			stored, err := session.promptStored(ctx, input.QueueID)
			if stored || err != nil {
				return input.QueueID, nil, err
			}
		}
	}
	session.mu.Lock()
	if err := session.closedErr(); err != nil {
		session.mu.Unlock()
		return "", nil, err
	}
	if !idempotent && !session.starting && !session.running && len(session.queue) == 0 {
		session.running, session.userCancelled = true, false
		session.mu.Unlock()
		result := make(chan error, 1)
		ctx = context.WithoutCancel(ctx)
		obs.Go("acp.turns", func() { session.runTurns(ctx, &input, result) })
		return "", result, nil
	}
	item := queuedPrompt{ID: input.QueueID, Input: input, Steer: steer}
	if item.ID == "" {
		item.ID = rand.Text()
	}
	// The prompt entry that sends it carries the ID, which is what takes it off every viewer's queue.
	item.Input.QueueID = item.ID
	session.mu.Unlock()
	session.log.Append("queued_prompt", session.clock().UnixMilli(), queuedPromptPayload(item))
	if idempotent {
		if err := session.log.Flush(); err != nil {
			return "", nil, err
		}
		session.seenPrompts[item.ID] = struct{}{}
	}
	session.mu.Lock()
	session.queue = placeQueued(session.queue, item)
	running := session.running
	session.mu.Unlock()
	session.afterQueued(ctx, item.Steer, running)
	return item.ID, nil, nil
}

// afterQueued starts the queue, hands an agent message to a running turn, or steers the current
// turn. An agent that serves _session/steering takes a steer into its turn; any other is
// interrupted with session/cancel first, which the UI warns about. The caller holds queueMu.
func (session *ConversationSession) afterQueued(ctx context.Context, steer, running bool) {
	if !running {
		session.maybeDrain()
		return
	}
	if !steer {
		session.deliverDuringTurnLocked(false)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	session.mu.Lock()
	broker, steerTurn := session.broker, session.steer
	var item queuedPrompt
	if len(session.queue) > 0 {
		item = session.queue[0]
	}
	session.mu.Unlock()
	if steerTurn != nil && item.Steer {
		session.steerInto(ctx, steerTurn, item)
		return
	}
	session.log.Append("notice", session.clock().UnixMilli(), mustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "info", "title": "Interrupted to steer",
	}))
	if err := broker.Cancel(ctx); err != nil {
		_, event := obs.Start(ctx, "acp.turn", "steer_cancel")
		event.Set("session_id", session.sessionID)
		event.Fail(err)
		event.End()
	}
}

// steerInto hands a steered prompt to the running turn. While queueMu is held no turn loop can
// send it too; when no turn takes it, it stays first in the queue for the next turn.
func (session *ConversationSession) steerInto(ctx context.Context, steerTurn func(context.Context, acp.PromptInput) (bool, error), item queuedPrompt) {
	session.writes.RLock()
	injected, err := steerTurn(ctx, item.Input)
	session.writes.RUnlock()
	if err != nil {
		_, event := obs.Start(ctx, "acp.turn", "steer")
		event.Set("session_id", session.sessionID)
		event.Fail(err)
		event.End()
		session.log.Append("notice", session.clock().UnixMilli(), mustRawJSON(map[string]string{
			"sessionUpdate": "notice", "severity": "warning", "title": "The message goes in when this turn ends", "description": err.Error(),
		}))
		return
	}
	if injected {
		session.mu.Lock()
		session.queue = slices.DeleteFunc(session.queue, func(queued queuedPrompt) bool { return queued.ID == item.ID })
		session.mu.Unlock()
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
	session.log.Append("queued_prompt", session.clock().UnixMilli(), queuedPromptPayload(item))
	session.afterQueued(ctx, item.Steer, running)
	return nil
}

// deliverDuringTurn hands queued prompts to an agent that takes them in while its turn runs
// (QueuesPrompts): agent messages at once, and the next user prompt when a model cycle ends, so
// neither waits for a long turn such as a native /goal. User prompts stay editable until then.
// Other agents get both from runTurns when the turn ends.
func (session *ConversationSession) deliverDuringTurn(cycleEnd bool) {
	session.queueMu.Lock()
	defer session.queueMu.Unlock()
	session.deliverDuringTurnLocked(cycleEnd)
}

// deliverDuringTurnLocked is deliverDuringTurn with queueMu held.
func (session *ConversationSession) deliverDuringTurnLocked(cycleEnd bool) {
	session.mu.Lock()
	// An open permission request does not hold them: the agent queues them behind its answer.
	if !session.queuesPrompts || session.delivering == 0 || session.closedErr() != nil {
		session.mu.Unlock()
		return
	}
	var send []queuedPrompt
	session.queue = slices.DeleteFunc(session.queue, func(item queuedPrompt) bool {
		take := !item.Steer && (item.Input.From != nil || cycleEnd)
		if take {
			cycleEnd = cycleEnd && item.Input.From != nil // one user prompt per cycle
			send = append(send, item)
		}
		return take
	})
	broker := session.broker
	session.delivering += len(send)
	session.mu.Unlock()
	for _, item := range send {
		sent, answered := make(chan struct{}), make(chan struct{})
		ctx := acp.WithAccepted(context.Background(), sync.OnceFunc(func() { close(sent) }))
		obs.Go("acp.deliver", func() {
			defer close(answered)
			session.writes.RLock()
			defer session.writes.RUnlock()
			if err := session.deliver(ctx, broker, item.Input); err != nil {
				select {
				case <-sent:
				default:
					session.dropUnsent(item.ID, err)
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
func (session *ConversationSession) dropUnsent(id string, err error) {
	session.log.Append("notice", session.clock().UnixMilli(), mustRawJSON(map[string]string{
		"sessionUpdate": "notice", "severity": "error", "title": "The agent could not run this prompt", "description": err.Error(),
	}))
	session.logDequeued(id, "failed")
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
		accepted, report := false, first
		// The HTTP request waits only until the prompt is logged; later failures land in the event log.
		turnCtx := acp.WithAccepted(ctx, func() {
			if !accepted && report != nil {
				report <- nil
			}
			accepted = true
		})
		err := session.Prompt(turnCtx, *input)
		session.awaitDelivered()
		if err != nil && !accepted && input.QueueID != "" {
			session.dropUnsent(input.QueueID, err)
		}
		if first != nil {
			if !accepted {
				first <- err
			}
			first = nil
		}
		session.finishTurn(ctx, err)
		session.retryAfterNetworkDrop(err)
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
	return item.Input, true
}

// maybeDrain starts the turn loop when the agent is free and a queued prompt or goal turn is due.
func (session *ConversationSession) maybeDrain() {
	session.mu.Lock()
	start := !session.starting && !session.running && session.closedErr() == nil && session.state != "needs_you" &&
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
func (session *ConversationSession) ResumeTurns() {
	session.mu.Lock()
	session.starting = false
	session.mu.Unlock()
	session.maybeDrain()
}

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
		session.seenPrompts[meta.QueueID] = struct{}{}
		session.queue = placeQueued(session.queue, queuedPrompt{ID: meta.QueueID, Input: input, Steer: meta.Steer})
	case "prompt", "dequeued_prompt":
		var meta struct {
			QueueID string `json:"queueId"`
		}
		if json.Unmarshal(entry.Payload, &meta) == nil && meta.QueueID != "" {
			session.seenPrompts[meta.QueueID] = struct{}{}
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
