package acp

import (
	"context"
	"errors"
	"sync"
)

// Broker forwards conversation control and coordinates per-request answers.
type Broker struct {
	prompt    func(context.Context, PromptInput) error
	cancel    func(context.Context) error
	answer    func(context.Context, string, string) error
	setConfig func(context.Context, string, string) error

	mu      sync.Mutex
	pending map[string]*pendingAnswer
}

type pendingAnswer struct {
	done      chan struct{}
	optionID  string
	answered  bool
	answering bool
}

func NewBroker(
	prompt func(context.Context, PromptInput) error,
	cancel func(context.Context) error,
	answer func(context.Context, string, string) error,
	setConfig func(context.Context, string, string) error,
) *Broker {

	return &Broker{
		prompt:    prompt,
		cancel:    cancel,
		answer:    answer,
		setConfig: setConfig,
		pending:   make(map[string]*pendingAnswer),
	}
}

// Begin starts a request lifecycle. Active IDs cannot be reused.
func (b *Broker) Begin(requestID string) error {
	if b == nil {
		return errors.New("permission broker is unavailable")
	}
	if requestID == "" {
		return errors.New("request id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[requestID]; ok {
		return errors.New("request id is already active")
	}
	b.pending[requestID] = &pendingAnswer{done: make(chan struct{})}
	return nil
}

// End consumes the result and permits a later request to reuse the ID.
func (b *Broker) End(requestID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if pending := b.pending[requestID]; pending != nil {
		delete(b.pending, requestID)
		if !pending.answered {
			close(pending.done)
		}
	}
}

func (b *Broker) SetConfig(ctx context.Context, key, value string) error {
	if b == nil || b.setConfig == nil {
		return errors.New("session config options are unavailable")
	}
	return b.setConfig(ctx, key, value)
}

func (b *Broker) Prompt(ctx context.Context, input PromptInput) error {
	if b == nil || b.prompt == nil {
		return nil
	}
	return b.prompt(ctx, input)
}

func (b *Broker) Cancel(ctx context.Context) error {
	if b == nil || b.cancel == nil {
		return nil
	}
	return b.cancel(ctx)
}

// AnswerPermission forwards and records the first answer for an active request.
func (b *Broker) AnswerPermission(ctx context.Context, requestID, optionID string) error {
	if b == nil || requestID == "" {
		return errors.New("permission request is not pending")
	}
	b.mu.Lock()
	pending := b.pending[requestID]
	if pending == nil || pending.answered || pending.answering {
		b.mu.Unlock()
		return errors.New("permission request is not pending")
	}
	pending.answering = true
	b.mu.Unlock()

	if b.answer != nil {
		if err := b.answer(ctx, requestID, optionID); err != nil {
			b.mu.Lock()
			if b.pending[requestID] == pending {
				pending.answering = false
			}
			b.mu.Unlock()
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending[requestID] != pending || !pending.answering {
		return errors.New("permission request is not pending")
	}
	pending.answering = false
	pending.optionID = optionID
	pending.answered = true
	close(pending.done)
	return nil
}

// Answer records the first option ID for an active request.
func (b *Broker) Answer(requestID, optionID string) bool {
	if b == nil || requestID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := b.pending[requestID]
	if pending == nil || pending.answered || pending.answering {
		return false
	}
	pending.optionID = optionID
	pending.answered = true
	close(pending.done)
	return true
}

// Wait blocks until the active request is answered or ctx is done.
func (b *Broker) Wait(ctx context.Context, requestID string) (string, error) {
	if b == nil {
		return "", context.Canceled
	}
	if requestID == "" {
		return "", errors.New("request id is required")
	}
	b.mu.Lock()
	pending := b.pending[requestID]
	b.mu.Unlock()
	if pending == nil {
		return "", errors.New("request is not pending")
	}

	select {
	case <-ctx.Done():
	case <-pending.done:
	}
	b.mu.Lock()
	optionID, answered := pending.optionID, pending.answered
	b.mu.Unlock()
	if answered {
		return optionID, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("request is no longer pending")
}
