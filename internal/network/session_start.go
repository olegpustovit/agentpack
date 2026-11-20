package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/state"
)

// launchSession starts a terminal session synchronously. A chat answers at once: its row is written
// as "starting" with a gateway-chosen ID, and pack sync plus the agent launch carry on in the
// background, so the browser can open the chat and type while the agent comes up.
func (g *Gateway) launchSession(ctx context.Context, info SessionInfo, forkSessionID string) (SessionInfo, error) {
	if info.Transport != "acp" {
		id, err := g.options.Start(ctx, info, forkSessionID)
		info.ID = id
		return info, err
	}
	folderID, err := FolderIdentity(info.Directory)
	if err != nil {
		return SessionInfo{}, err
	}
	// The row is the folder's newest recent entry from now on, so it must carry the folder's identity.
	info.FolderID = folderID
	info.ID = newSessionID()
	info.ConversationState = "starting"
	info.StartedAt = time.Now().UTC()
	info.LastUsed = info.StartedAt
	if err := upsertSession(ctx, g.auth.store.DB, info); err != nil {
		return SessionInfo{}, err
	}
	g.startInBackground(ctx, info, forkSessionID, "")
	return info, nil
}

// startInBackground launches the worker for a "starting" chat row, then sends prompt if there is one;
// a failure ends the row with the reason.
func (g *Gateway) startInBackground(ctx context.Context, info SessionInfo, forkSessionID, prompt string) {
	background := context.WithoutCancel(ctx)
	g.starting.Add(1)
	obs.Go("gateway.start_session", func() {
		defer g.starting.Done()
		if _, err := g.options.Start(background, info, forkSessionID); err != nil {
			_, event := obs.Start(background, "gateway", "start_session")
			event.Set("session_id", info.ID)
			event.Set("harness", info.Harness)
			event.Fail(err)
			event.End()
			g.failStart(background, info, err)
			return
		}
		if prompt == "" {
			return
		}
		if err := ConversationPrompt(background, g.options.Home, info.ID, prompt); err != nil {
			_, event := obs.Start(background, "gateway", "resume_prompt")
			event.Set("session_id", info.ID)
			event.Fail(err)
			event.End()
		}
	})
}

var (
	errResumeConflict = errors.New("This chat is already running or starting.")
	errNotResumable   = errors.New("This chat cannot be resumed.")
)

// resumeSession brings a stopped chat back under its own ID: the worker reloads the agent's
// conversation, continues the same transcript and gets prompt (if any) once it runs.
func (g *Gateway) resumeSession(ctx context.Context, id, prompt string) (SessionInfo, error) {
	g.chatLifecycle.Lock()
	defer g.chatLifecycle.Unlock()
	// A worker that died without ending its row is settled first, so the claim below sees it stopped.
	g.reapStoppedChats(ctx)
	info, err := storedSession(ctx, g.auth.store.Reader, id)
	if err != nil {
		return SessionInfo{}, err
	}
	local, err := g.auth.store.NodeID(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	if info.NodeID != local || info.Transport != "acp" || info.AgentSessionID == "" || !info.ArchivedAt.IsZero() {
		return SessionInfo{}, errNotResumable
	}
	if !g.knownDirectory(info.Directory) && !g.knownWorktree(ctx, info.Directory) {
		return SessionInfo{}, errors.New("choose a recent folder from this node")
	}
	// The claim is atomic, so two resumes cannot start two workers for one chat.
	result, err := g.auth.store.DB.ExecContext(ctx, "UPDATE sessions SET conversation_state='starting',ended_at=0 WHERE id=? AND ended_at>0 AND archived_at=0 AND node_id="+state.LocalNodeSQL, id)
	if err != nil {
		return SessionInfo{}, err
	}
	if claimed, err := result.RowsAffected(); err != nil || claimed != 1 {
		return SessionInfo{}, errors.Join(errResumeConflict, err)
	}
	info.ConversationState = "starting"
	info.EndedAt = time.Time{}
	info.Reachable = true
	info.LastUsed = time.Now().UTC()
	g.startInBackground(ctx, info, "", prompt)
	return info, nil
}

// failStart leaves the error in the chat's transcript and ends the row. The notice also keeps the
// row listed, which an empty chat otherwise would not be.
func (g *Gateway) failStart(ctx context.Context, info SessionInfo, cause error) {
	err := func() error {
		if err := recordStop(ctx, g.auth.store, info.ID, fmt.Sprintf("Could not start %s", info.Harness), cause.Error()); err != nil {
			return err
		}
		// A worker that failed early already ended the row, and hid it for having no prompt yet.
		if _, err := g.auth.store.DB.ExecContext(ctx, "UPDATE sessions SET hidden=0 WHERE id=? AND archived_at=0 AND node_id="+state.LocalNodeSQL, info.ID); err != nil {
			return err
		}
		return endSession(ctx, g.auth.store.DB, info.ID, TerminalState{ExitCode: 1})
	}()
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "record_start_failure")
		event.Set("session_id", info.ID)
		event.Fail(err)
		event.End()
	}
}

// recordStop ends a chat's transcript with why it stopped. Only call it while no worker writes the log.
func recordStop(ctx context.Context, store *state.Store, id, title, description string) error {
	log, err := newConversationLog(ctx, store, id, func(error) {})
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	log.Append("notice", now, mustRawJSON(map[string]string{"sessionUpdate": "notice", "severity": "error", "title": title, "description": description}))
	log.Append("agent_exit", now, json.RawMessage(`{}`))
	return log.Flush()
}

// reapStoppedChats ends rows whose worker died without ending them, then settles ended chats that
// still claim a turn, a question or subagents: such a worker never recorded its exit. The caller
// holds chatLifecycle, so a resume cannot claim a row this is writing.
func (g *Gateway) reapStoppedChats(ctx context.Context) {
	g.importLegacySessions(ctx)
	rows, err := querySessions(ctx, g.auth.store.Reader, "SELECT "+sessionColumns+` FROM sessions WHERE transport='acp' AND ended_at>0 AND archived_at=0 AND node_id=`+state.LocalNodeSQL+`
 AND (conversation_state<>'idle' OR subagents<>'' OR json_extract(state,'$.running'))`)
	for _, info := range rows {
		err = errors.Join(err, recordStop(ctx, g.auth.store, info.ID, fmt.Sprintf("%s stopped", info.Harness), "Its process ended before the turn finished. Send a message to resume the chat."))
		info.State.Running, info.State.Restarting = false, false
		body, marshalErr := json.Marshal(info.State)
		if marshalErr == nil {
			_, marshalErr = g.auth.store.DB.ExecContext(ctx, "UPDATE sessions SET state=?,conversation_state='idle',subagents='' WHERE id=? AND ended_at>0 AND node_id="+state.LocalNodeSQL, string(body), info.ID)
		}
		err = errors.Join(err, marshalErr)
	}
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "reap_stopped_chats")
		event.Fail(err)
		event.End()
	}
}

// runReaper notices workers that died while this gateway runs.
func (g *Gateway) runReaper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.chatLifecycle.Lock()
			g.reapStoppedChats(ctx)
			g.chatLifecycle.Unlock()
		}
	}
}

// endLeftoverStarts ends chats a previous gateway was still starting when it stopped. A worker that
// does come up rewrites its row, which clears ended_at again.
func (g *Gateway) endLeftoverStarts(ctx context.Context) {
	rows, err := querySessions(ctx, g.auth.store.Reader, "SELECT "+sessionColumns+" FROM sessions WHERE conversation_state='starting' AND ended_at=0 AND node_id="+state.LocalNodeSQL)
	for _, info := range rows {
		if _, statErr := os.Stat(filepath.Join(g.options.Home, "network", "sessions", info.ID, "worker.json")); statErr == nil {
			continue
		}
		g.failStart(ctx, info, fmt.Errorf("the gateway restarted before %s was ready", info.Harness))
	}
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "end_leftover_starts")
		event.Fail(err)
		event.End()
	}
}
