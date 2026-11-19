package network

import (
	"context"
	"encoding/json"
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
		}
	})
	return info, nil
}

// failStart leaves the error in the chat's transcript and ends the row. The notice also keeps the
// row listed, which an empty chat otherwise would not be.
func (g *Gateway) failStart(ctx context.Context, info SessionInfo, cause error) {
	err := func() error {
		log, err := newConversationLog(ctx, g.auth.store, info.ID, func(error) {})
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		log.Append("notice", now, mustRawJSON(map[string]string{
			"sessionUpdate": "notice", "severity": "error",
			"title": fmt.Sprintf("Could not start %s", info.Harness), "description": cause.Error(),
		}))
		log.Append("agent_exit", now, json.RawMessage(`{}`))
		if err := log.Flush(); err != nil {
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
