package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

type chatStartRequest struct {
	ID             string `json:"id,omitempty"`
	Directory      string `json:"directory"`
	Harness        string `json:"harness"`
	Mode           string `json:"mode"`
	Transport      string `json:"transport,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Resume         string `json:"resume,omitempty"`
	ParentID       string `json:"parent_id,omitempty"`
	Place          string `json:"place,omitempty"`
	FirstMessage   string `json:"first_message,omitempty"`
	Tools          *bool  `json:"tools,omitempty"`
	// Worktree is "new" or an existing worktree of Directory; Carry brings uncommitted changes.
	Worktree     string `json:"worktree,omitempty"`
	WorktreeName string `json:"worktree_name,omitempty"`
	Carry        bool   `json:"carry,omitempty"`

	forkSessionID  string
	definitionRoot string
}

var (
	errChatStartClaim  = errors.New("session ID already claimed")
	errInvalidChatID   = errors.New("invalid chat ID")
	errResumeOwnFolder = errors.New("a resumed conversation keeps its own folder")
)

func (g *Gateway) claimChatStart(ctx context.Context, actor session.Actor, input chatStartRequest) (session.SessionInfo, error) {
	token := strings.TrimPrefix(input.ID, "session-")
	if input.Transport != "acp" || !network.ValidSessionID(input.ID) || len(token) != 32 || token != strings.ToLower(token) {
		return session.SessionInfo{}, errInvalidChatID
	}
	if _, err := hex.DecodeString(token); err != nil {
		return session.SessionInfo{}, errInvalidChatID
	}
	fingerprint := sha256.Sum256(network.MustRawJSON(struct {
		Actor session.Actor
		Input chatStartRequest
		Fork  string `json:",omitempty"`
	}{actor, input, input.forkSessionID}))
	if info, err := readChatStartClaim(ctx, g.store.Reader, input.ID, fingerprint[:]); !errors.Is(err, sql.ErrNoRows) {
		return info, err
	}
	info := session.SessionInfo{
		ID: input.ID, Directory: filepath.Clean(input.Directory), DefinitionRoot: input.definitionRoot, Harness: input.Harness,
		Mode: input.Mode, Transport: input.Transport, AgentSessionID: network.CmpOr(input.AgentSessionID, input.Resume),
		ParentID: input.ParentID, Tools: input.Tools == nil || *input.Tools, StartedBy: &actor,
		ConversationState: "starting", Reachable: true,
	}
	if input.Worktree != "" && info.AgentSessionID != "" {
		return session.SessionInfo{}, errResumeOwnFolder
	}
	source := info
	if input.forkSessionID != "" {
		source.AgentSessionID = input.forkSessionID
	}
	if err := network.AuthorizeACPResume(ctx, g.store, actor, source); err != nil {
		return session.SessionInfo{}, err
	}
	if main, _, err := worktreeMain(ctx, g.options.Home, info.Directory); input.Worktree == "" && err == nil && g.startableDirectory(ctx, actor, main) {
		info.WorktreeOf = main
		if info.DefinitionRoot == "" {
			info.DefinitionRoot = g.definitionRootFor(ctx, main)
		}
	} else if !g.startableDirectory(ctx, actor, info.Directory) {
		return session.SessionInfo{}, errUnknownFolder
	}
	var err error
	if info.FolderID, err = network.FolderIdentity(info.Directory); err != nil {
		return session.SessionInfo{}, err
	}
	if info.NodeID, err = g.nodeID(ctx); err != nil {
		return session.SessionInfo{}, err
	}
	info.StartedAt = time.Now().UTC()
	info.LastUsed = info.StartedAt
	args, err := session.SessionValues(info)
	if err != nil {
		return session.SessionInfo{}, err
	}
	inserted := false
	err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, session.InsertSessionSQL+" ON CONFLICT(id) DO NOTHING", args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		inserted = true
		if _, err := tx.ExecContext(ctx, "INSERT INTO session_start_requests VALUES (?,?)", info.ID, fingerprint[:]); err != nil {
			return err
		}
		if prompt := strings.TrimSpace(input.FirstMessage); prompt != "" {
			return appendStartingPrompt(ctx, tx, info.ID, network.QueuedPrompt{ID: token, Input: acp.PromptInput{Text: prompt, QueueID: token}})
		}
		return nil
	})
	if err != nil {
		return session.SessionInfo{}, err
	}
	if !inserted {
		existing, err := readChatStartClaim(ctx, g.store.Reader, input.ID, fingerprint[:])
		if errors.Is(err, sql.ErrNoRows) {
			err = errChatStartClaim
		}
		return existing, err
	}
	g.startInBackground(ctx, info, input.forkSessionID, &input)
	return info, nil
}

func readChatStartClaim(ctx context.Context, db *sql.DB, id string, fingerprint []byte) (session.SessionInfo, error) {
	var existing []byte
	if err := db.QueryRowContext(ctx, "SELECT fingerprint FROM session_start_requests WHERE session_id=?", id).Scan(&existing); err != nil {
		return session.SessionInfo{}, err
	}
	if !bytes.Equal(existing, fingerprint) {
		return session.SessionInfo{}, errChatStartClaim
	}
	return session.StoredSession(ctx, db, id)
}

// launchSession starts a terminal session synchronously. A chat answers at once: its row is written
// as "starting" with a gateway-chosen ID, and pack sync plus the agent launch carry on in the
// background, so the browser can open the chat and type while the agent comes up.
func (g *Gateway) launchSession(ctx context.Context, info session.SessionInfo, forkSessionID string) (session.SessionInfo, error) {
	if info.Transport != "acp" {
		id, err := g.options.Start(ctx, info, forkSessionID)
		info.ID = id
		return info, err
	}
	info.ID = network.NewSessionID()
	info.ConversationState = "starting"
	info.StartedAt = time.Now().UTC()
	info.LastUsed = info.StartedAt
	if err := session.UpsertSession(ctx, g.store.DB, info); err != nil {
		return session.SessionInfo{}, err
	}
	g.startInBackground(ctx, info, forkSessionID, nil)
	return info, nil
}

// startInBackground prepares the worker for a starting chat. A failure ends the row with the reason.
// A cold start may need the npm tree's five-minute download budget before the ACP handshake.
const chatStartTimeout = 7 * time.Minute

const interruptedStartGrace = 2 * time.Minute

func (g *Gateway) startInBackground(ctx context.Context, info session.SessionInfo, forkSessionID string, request *chatStartRequest) {
	if !g.beginBackground(&g.starting) {
		return
	}
	background := context.WithoutCancel(ctx)
	g.startingChats.Store(info.ID, struct{}{})
	obs.Go("gateway.start_session", func() {
		defer g.starting.Done()
		defer g.startingChats.Delete(info.ID)
		startCtx, cancel := context.WithTimeout(background, chatStartTimeout)
		defer cancel()
		var err error
		if request != nil && request.Worktree != "" {
			main := info.Directory
			info.Directory, err = g.newSessionWorktree(startCtx, *info.StartedBy, main, request.Worktree, request.WorktreeName, request.Carry)
			if err == nil {
				info.FolderID, err = network.FolderIdentity(info.Directory)
			}
			if err == nil {
				info.WorktreeOf, _, err = worktreeMain(startCtx, g.options.Home, info.Directory)
			}
			if err == nil {
				info.DefinitionRoot = g.definitionRootFor(startCtx, main)
				_, err = g.store.DB.ExecContext(startCtx, "UPDATE sessions SET directory=?,folder_id=?,worktree_of=?,definition_root=? WHERE id=? AND node_id="+state.LocalNodeSQL,
					info.Directory, info.FolderID, info.WorktreeOf, info.DefinitionRoot, info.ID)
			}
		}
		if err == nil {
			_, err = g.options.Start(startCtx, info, forkSessionID)
		}
		if err != nil {
			_, event := obs.Start(background, "gateway", "start_session")
			event.Set("session_id", info.ID)
			event.Set("harness", info.Harness)
			event.Fail(err)
			event.End()
			g.failStart(background, info, err)
		}
	})
}

func (g *Gateway) stopSession(ctx context.Context, id, requestID string) error {
	if requestID == "" {
		return network.StopWorker(ctx, g.options.Home, id)
	}
	record, err := network.ReadWorkerRecord(g.options.Home, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fingerprint := sha256.Sum256([]byte(record.Secret))
	var claimed []byte
	if err := g.store.DB.QueryRowContext(ctx, `INSERT INTO session_stop_requests(session_id,request_id,worker_fingerprint)
		VALUES (?,?,?) ON CONFLICT(session_id,request_id) DO UPDATE SET request_id=excluded.request_id RETURNING worker_fingerprint`,
		id, requestID, fingerprint[:]).Scan(&claimed); err != nil {
		return err
	}
	if record.Secret == "" || !bytes.Equal(claimed, fingerprint[:]) {
		return nil
	}
	return network.ControlWorkerRecord(ctx, record, "stop", requestID)
}

var (
	errResumeConflict = errors.New("This chat is already running or starting.")
	errNotResumable   = errors.New("This chat cannot be resumed.")
	errUnknownFolder  = errors.New("choose a recent folder from this node")
)

// resumeSession brings a stopped chat back under its own ID: the worker reloads the agent's
// conversation, continues the same transcript and gets prompt (if any) once it runs.
func (g *Gateway) resumeSession(ctx context.Context, id, prompt, queueID string) (session.SessionInfo, error) {
	g.chatLifecycle.Lock()
	defer g.chatLifecycle.Unlock()
	// A worker that died without ending its row is settled first, so the claim below sees it stopped.
	// The person's own message resumes this chat, so the reaper doesn't resume it too.
	g.reapStoppedChatsExcept(ctx, id)
	return g.claimResume(ctx, id, prompt, queueID)
}

// claimResume restarts a stopped chat; the caller holds chatLifecycle.
func (g *Gateway) claimResume(ctx context.Context, id, prompt, queueID string) (session.SessionInfo, error) {
	info, err := session.StoredSession(ctx, g.store.Reader, id)
	if err != nil {
		return session.SessionInfo{}, err
	}
	local, err := g.nodeID(ctx)
	if err != nil {
		return session.SessionInfo{}, err
	}
	// A chat whose agent never started (it wasn't signed in, say) has no agent session; it starts fresh.
	if info.NodeID != local || info.Transport != "acp" || !info.ArchivedAt.IsZero() {
		return session.SessionInfo{}, errNotResumable
	}
	if queueID != "" && prompt != "" {
		seen, err := network.StoredPromptID(ctx, g.store, id, queueID)
		if err != nil || seen {
			return info, err
		}
	}
	if info.EndedAt.IsZero() {
		return session.SessionInfo{}, errResumeConflict
	}
	if !g.knownDirectory(ctx, info.Directory) && !g.knownWorktree(ctx, info.Directory) {
		return session.SessionInfo{}, errUnknownFolder
	}
	// The claim is atomic, so two resumes cannot start two workers for one chat.
	now := time.Now().UTC()
	err = state.Transaction(ctx, g.store.DB, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE sessions SET conversation_state='starting',ended_at=0,last_used=? WHERE id=? AND ended_at>0 AND archived_at=0 AND node_id="+state.LocalNodeSQL, session.SessionTimestamp(now), id)
		if err != nil {
			return err
		}
		if claimed, err := result.RowsAffected(); err != nil || claimed != 1 {
			return errors.Join(errResumeConflict, err)
		}
		if prompt != "" {
			if queueID == "" {
				queueID = rand.Text()
			}
			return appendStartingPrompt(ctx, tx, id, network.QueuedPrompt{ID: queueID, Input: acp.PromptInput{Text: prompt, QueueID: queueID}})
		}
		return nil
	})
	if err != nil {
		return session.SessionInfo{}, err
	}
	info.ConversationState = "starting"
	info.EndedAt = time.Time{}
	info.Reachable = true
	info.LastUsed = now
	g.startInBackground(ctx, info, "", nil)
	return info, nil
}

// switchHarness hands a chat to another agent. The old agent stops; a fresh session of the new one
// continues the same transcript, and its first prompt carries what came before (handoffContext).
func (g *Gateway) switchHarness(ctx context.Context, id, harness string) (session.SessionInfo, error) {
	g.chatLifecycle.Lock()
	defer g.chatLifecycle.Unlock()
	info, err := session.StoredSession(ctx, g.store.Reader, id)
	if err != nil {
		return session.SessionInfo{}, err
	}
	if info.Transport != "acp" || !info.ArchivedAt.IsZero() {
		return session.SessionInfo{}, errNotResumable
	}
	if info.EndedAt.IsZero() {
		if err := network.StopWorker(ctx, g.options.Home, id); err != nil {
			return session.SessionInfo{}, err
		}
		// The worker ends the row as it exits, just after it answers the stop.
		for deadline := time.Now().Add(15 * time.Second); info.EndedAt.IsZero() && time.Now().Before(deadline); {
			select {
			case <-ctx.Done():
				return session.SessionInfo{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			if info, err = session.StoredSession(ctx, g.store.Reader, id); err != nil {
				return session.SessionInfo{}, err
			}
		}
	}
	result, err := g.store.DB.ExecContext(ctx, "UPDATE sessions SET harness=?,agent_session_id='',agent_capabilities='' WHERE id=? AND ended_at>0 AND archived_at=0 AND node_id="+state.LocalNodeSQL, harness, id)
	if err != nil {
		return session.SessionInfo{}, err
	}
	if switched, err := result.RowsAffected(); err != nil || switched != 1 {
		return session.SessionInfo{}, errors.Join(errResumeConflict, err)
	}
	return g.claimResume(ctx, id, "", "")
}

// failStart leaves the error in the chat's transcript and ends the row. The notice also keeps the
// row listed, which an empty chat otherwise would not be.
func (g *Gateway) failStart(ctx context.Context, info session.SessionInfo, cause error) {
	err := func() error {
		notice := map[string]any{"title": fmt.Sprintf("Could not start %s", info.Harness), "description": cause.Error()}
		// The chat offers a sign-in in place of the error, with the agent's ways to sign in.
		if strings.Contains(cause.Error(), network.NotSignedIn) {
			notice["code"], notice["harness"], notice["methods"] = "auth_required", info.Harness, network.LoginMethods(info.Harness)
		}
		if err := recordStop(ctx, g.store, info.ID, notice); err != nil {
			return err
		}
		// A worker that failed early already ended the row, and hid it for having no prompt yet.
		if _, err := g.store.DB.ExecContext(ctx, "UPDATE sessions SET hidden=0 WHERE id=? AND archived_at=0 AND node_id="+state.LocalNodeSQL, info.ID); err != nil {
			return err
		}
		return session.EndSession(ctx, g.store.DB, info.ID, session.TerminalState{ExitCode: 1})
	}()
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "record_start_failure")
		event.Set("session_id", info.ID)
		event.Fail(err)
		event.End()
	}
}

// recordStop ends a chat's transcript with why it stopped. Only call it while no worker writes the log.
// It appends the two rows directly instead of restoring the replay tail into a log.
func recordStop(ctx context.Context, store *state.Store, id string, notice map[string]any) error {
	now := time.Now()
	notice["sessionUpdate"], notice["severity"] = "notice", "error"
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		for _, entry := range []acp.LogEntry{
			{TS: now.UnixMilli(), Kind: "notice", Payload: network.MustRawJSON(notice)},
			{TS: now.UnixMilli(), Kind: "agent_exit", Payload: json.RawMessage(`{}`)},
		} {
			if err := tx.QueryRowContext(ctx, `UPDATE sessions SET last_seq=last_seq+1,last_event_at=?
				WHERE id=? AND archived_at=0 AND node_id=`+state.LocalNodeSQL+` RETURNING last_seq`, now.UnixNano(), id).Scan(&entry.Seq); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO conversation_events(session_id,seq,body) VALUES (?,?,?)", id, entry.Seq, string(network.MustRawJSON(entry))); err != nil {
				return err
			}
		}
		return nil
	})
}

// reapStoppedChats ends rows whose worker died without ending them, then settles ended chats that
// still claim a turn, a question or subagents, or whose log has a turn with no end (a worker that
// could not save its exit, say while the database was locked): such a worker never recorded its
// exit. The caller holds chatLifecycle, so a resume cannot claim a row this is writing.
func (g *Gateway) reapStoppedChats(ctx context.Context) { g.reapStoppedChatsExcept(ctx, "") }

// reapStoppedChatsExcept never resumes manual, the chat its caller is about to resume.
func (g *Gateway) reapStoppedChatsExcept(ctx context.Context, manual string) {
	g.reapWorkerEndpoints(ctx)
	now := time.Now()
	rows, err := session.QuerySessions(ctx, g.store.Reader, "SELECT "+session.SessionColumns+` FROM sessions WHERE archived_at=0 AND node_id=`+state.LocalNodeSQL+`
 AND ((ended_at>0 AND transport='acp' AND (conversation_state<>'idle' OR subagents<>'' OR waiting<>'' OR json_extract(state,'$.running') OR ended_at>?2))
 OR (ended_at=0 AND last_used<?1 AND (transport<>'acp' OR conversation_state='starting')))`, session.SessionTimestamp(now.Add(-interruptedStartGrace)), session.SessionTimestamp(now.Add(-autoResumeWindow)))
	var resume []string
	for _, info := range rows {
		if info.EndedAt.IsZero() {
			if info.Transport != "acp" {
				err = errors.Join(err, g.reapUnpublishedWorker(ctx, info, now))
				continue
			}
			// The previous gateway can exit before its start writes a worker endpoint.
			if _, active := g.startingChats.Load(info.ID); !active {
				_, endpointErr := network.ReadWorkerRecord(g.options.Home, info.ID)
				if errors.Is(endpointErr, fs.ErrNotExist) {
					g.failStart(ctx, info, errors.New("Startup was interrupted before the agent connected. Send a message to try again."))
				}
			}
			continue
		}
		// A turn cut off by sleep, a reboot or a crash carries on by itself, once per stop window.
		auto := false
		if info.AgentSessionID != "" && info.ID != manual {
			var cutErr error
			auto, cutErr = cutOffTurn(ctx, g.store.Reader, info.ID, time.Now())
			err = errors.Join(err, cutErr)
		}
		// A recently ended chat that settled cleanly has nothing to reap.
		if !auto && info.ConversationState == "idle" && len(info.Subagents) == 0 && len(info.Waiting) == 0 && !info.State.Running {
			continue
		}
		description := "Its process ended before the turn finished. Send a message to resume the chat."
		if auto {
			description = "Its process ended before the turn finished, so the chat resumes by itself."
			resume = append(resume, info.ID)
		}
		err = errors.Join(err, recordStop(ctx, g.store, info.ID, map[string]any{"title": fmt.Sprintf("%s stopped", info.Harness), "description": description}))
		info.State.Running, info.State.Restarting = false, false
		body, marshalErr := json.Marshal(info.State)
		if marshalErr == nil {
			_, marshalErr = g.store.DB.ExecContext(ctx, "UPDATE sessions SET state=?,conversation_state='idle',subagents='',waiting='' WHERE id=? AND ended_at>0 AND node_id="+state.LocalNodeSQL, string(body), info.ID)
		}
		err = errors.Join(err, marshalErr)
	}
	for _, id := range resume {
		if _, resumeErr := g.claimResume(ctx, id, autoResumePrompt, ""); resumeErr != nil && !errors.Is(resumeErr, errResumeConflict) {
			err = errors.Join(err, resumeErr)
		}
	}
	if err != nil {
		_, event := obs.Start(ctx, "gateway", "reap_stopped_chats")
		event.Fail(err)
		event.End()
	}
}

const autoResumePrompt = "Your last turn was cut off because this machine went offline or restarted. Continue where you left off."

// autoResumeWindow keeps a chat whose agent dies again right after resuming from resuming forever.
const autoResumeWindow = 10 * time.Minute

// cutOffTurn reads from the log whether a stopped chat may resume by itself: its own last prompt has
// no turn end after it, and it has not resumed by itself within autoResumeWindow (the reaper logs
// autoResumePrompt as the chat's queued prompt when it does).
func cutOffTurn(ctx context.Context, db *sql.DB, id string, now time.Time) (bool, error) {
	var cut bool
	err := db.QueryRowContext(ctx, `SELECT
 coalesce((SELECT max(seq) FROM conversation_events WHERE session_id=?1 AND kind='prompt'
  AND json_extract(body,'$.payload._subagent') IS NULL),0) >
 coalesce((SELECT max(seq) FROM conversation_events WHERE session_id=?1 AND kind IN ('turn_end','agent_exit')
  AND json_extract(body,'$.payload._subagent') IS NULL),0)
 AND NOT EXISTS(SELECT 1 FROM conversation_events WHERE session_id=?1 AND kind='queued_prompt'
  AND json_extract(body,'$.payload.text.text')=?2 AND json_extract(body,'$.ts')>?3)`,
		id, autoResumePrompt, now.Add(-autoResumeWindow).UnixMilli()).Scan(&cut)
	return cut, err
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
	rows, err := session.QuerySessions(ctx, g.store.Reader, "SELECT "+session.SessionColumns+" FROM sessions WHERE conversation_state='starting' AND ended_at=0 AND node_id="+state.LocalNodeSQL)
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
