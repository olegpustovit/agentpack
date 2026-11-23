package network

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
)

// reloadPending reports a reload that was asked for and has not succeeded. It lives in the session
// directory, so a request that a worker crash or a later request replaced still converges.
func (worker *WorkerHTTP) reloadPending() bool {
	if worker.SessionDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(worker.SessionDir, "restart_pending"))
	return err == nil
}

func (worker *WorkerHTTP) setReloadPending(pending bool) error {
	if worker.SessionDir == "" {
		return nil
	}
	path := filepath.Join(worker.SessionDir, "restart_pending")
	if pending {
		return os.WriteFile(path, nil, 0600)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// reloadWhenIdle restarts the session now when nothing would be cut off, and otherwise records the
// reload for the next idle moment: a chat's (see ConversationSession.readyToReload), or the turn
// end a terminal agent's hooks report. deferred reports the latter.
func (worker *WorkerHTTP) reloadWhenIdle(ctx context.Context, operationID string) (deferred bool, err error) {
	conversation := worker.conversationSession()
	if conversation == nil {
		state, _ := worker.agentState.Load().(string)
		if state == string(StateWorking) || state == string(StateNeedsYou) {
			return true, worker.setReloadPending(true)
		}
		return false, worker.Restart(ctx, operationID)
	}
	// Under the lock that admits prompts: one that arrives now waits, then goes to the new agent.
	conversation.queueMu.Lock()
	defer conversation.queueMu.Unlock()
	if !conversation.readyToReload(worker.reloadLater) {
		return true, worker.setReloadPending(true)
	}
	return false, worker.Restart(ctx, operationID)
}

// reloadLater runs a deferred reload; Restart records its own failure.
func (worker *WorkerHTTP) reloadLater() {
	obs.Go("worker.restart_when_idle", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = worker.reloadWhenIdle(ctx, rand.Text())
	})
}

// reloadBeforePrompt reloads an idle chat before a prompt reaches it when a reload is pending or its
// environment moved since the agent started: the safety net for edits nothing announced. The check
// hashes content only after the launch fingerprint moved; a failed check never holds a prompt up.
func (worker *WorkerHTTP) reloadBeforePrompt(ctx context.Context) (reloaded bool, err error) {
	conversation := worker.conversationSession()
	if conversation == nil || worker.environment == nil && !worker.reloadPending() {
		return false, nil
	}
	conversation.queueMu.Lock()
	defer conversation.queueMu.Unlock()
	if !conversation.readyToReload(nil) {
		return false, nil // a running turn queues the prompt; the reload follows at its end
	}
	stale := worker.reloadPending()
	if !stale && worker.environment != nil {
		if stale, err = worker.environment.stale(); err != nil {
			event := worker.observe(ctx, "environment_check")
			event.Fail(err)
			event.End()
			return false, nil
		}
	}
	if !stale {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	return true, worker.Restart(ctx, rand.Text())
}
