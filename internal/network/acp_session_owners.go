package network

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
)

var (
	ErrACPResumeDenied       = errors.New("read-only actor cannot use this ACP conversation")
	ErrACPResumeOwnerStorage = errors.New("ACP session owner record is unavailable")
)

type acpConversationOwnerRecord struct {
	FolderID string        `json:"folder_id"`
	Harness  string        `json:"harness"`
	Owner    session.Actor `json:"owner"`
}

func acpOwnerKey(folderID, harness, sessionID string) (key, canonical string, err error) {
	if folderID == "" || sessionID == "" {
		return "", "", errors.New("ACP owner key is incomplete")
	}
	target, err := HarnessTarget(harness)
	if err != nil {
		return "", "", err
	}
	canonical = string(target)
	digest := sha256.Sum256([]byte(folderID + "\x00" + canonical + "\x00" + sessionID))
	return hex.EncodeToString(digest[:]), canonical, nil
}

func decodeACPAccountOwner(body, email string) (session.Actor, error) {
	var owner session.Actor
	if err := json.Unmarshal([]byte(body), &owner); err != nil {
		return owner, errors.Join(ErrACPResumeOwnerStorage, err)
	}
	if owner.User == "" || owner.Node == "" && owner.StableNodeID == "" {
		return owner, ErrACPResumeOwnerStorage
	}
	if email != "" {
		normalized, err := session.NormalizeEmail(owner.User)
		if err != nil || normalized != email {
			return owner, ErrACPResumeOwnerStorage
		}
	}
	return owner, nil
}

func loadACPConversationOwner(ctx context.Context, tx *sql.Tx, folderID, harness, sessionID string) (session.Actor, bool, error) {
	key, canonical, err := acpOwnerKey(folderID, harness, sessionID)
	if err != nil {
		return session.Actor{}, false, err
	}
	var body, email, storedFolder, storedHarness string
	var owner session.Actor
	found := false
	err = tx.QueryRowContext(ctx, "SELECT started_by,owner_email,folder_id,harness FROM acp_session_owners WHERE key=? AND node_id="+state.LocalNodeSQL, key).Scan(&body, &email, &storedFolder, &storedHarness)
	if err == nil {
		if storedFolder != folderID || storedHarness != canonical {
			return session.Actor{}, false, ErrACPResumeOwnerStorage
		}
		owner, err = decodeACPAccountOwner(body, email)
		if err != nil {
			return session.Actor{}, false, err
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return session.Actor{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT harness,started_by,owner_email,archived_at FROM sessions
        WHERE node_id=`+state.LocalNodeSQL+` AND folder_id=? AND agent_session_id=? AND transport='acp' ORDER BY started_at,id`, folderID, sessionID)
	if err != nil {
		return session.Actor{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, body, email string
		var archivedAt int64
		if err := rows.Scan(&name, &body, &email, &archivedAt); err != nil {
			return session.Actor{}, false, err
		}
		target, err := HarnessTarget(name)
		if err != nil || string(target) != canonical {
			continue
		}
		if archivedAt != 0 {
			return session.Actor{}, false, session.ErrSessionArchived
		}
		if body == "" || body == "null" {
			continue
		}
		candidate, err := decodeACPAccountOwner(body, email)
		if err != nil {
			return session.Actor{}, false, err
		}
		if found && !candidate.CanWriteSession(session.SessionInfo{StartedBy: &owner}) {
			return session.Actor{}, false, ErrACPResumeOwnerStorage
		}
		if !found {
			owner, found = candidate, true
		}
	}
	return owner, found, rows.Err()
}

func ReadACPConversationOwner(ctx context.Context, store *state.Store, folderID, harness, sessionID string) (session.Actor, bool, error) {
	tx, err := store.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return session.Actor{}, false, errors.Join(ErrACPResumeOwnerStorage, err)
	}
	defer tx.Rollback()
	owner, found, err := loadACPConversationOwner(ctx, tx, folderID, harness, sessionID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		err = errors.Join(ErrACPResumeOwnerStorage, err)
	}
	return owner, found, err
}

func AuthorizeACPResume(ctx context.Context, store *state.Store, actor session.Actor, info session.SessionInfo) error {
	if info.AgentSessionID == "" {
		return nil
	}
	target, err := HarnessTarget(info.Harness)
	if err != nil {
		return err
	}
	rows, err := store.Reader.QueryContext(ctx, "SELECT harness FROM sessions WHERE agent_session_id=? AND archived_at>0", info.AgentSessionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		canonical, err := HarnessTarget(name)
		if err == nil && canonical == target {
			rows.Close()
			return session.ErrSessionArchived
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || info.Transport != "acp" {
		return err
	}
	folderID, err := FolderIdentity(info.Directory)
	if err != nil {
		return err
	}
	owner, found, err := ReadACPConversationOwner(ctx, store, folderID, info.Harness, info.AgentSessionID)
	if err != nil {
		return err
	}
	if !found {
		if actor.Kind == "peer" {
			return ErrACPResumeDenied
		}
		return nil
	}
	if !actor.CanWriteSession(session.SessionInfo{StartedBy: &owner}) {
		return ErrACPResumeDenied
	}
	return nil
}

// A binding keeps immutable native ownership even after the chats that recorded it are gone.
func insertOwnerBinding(ctx context.Context, tx *sql.Tx, key string, record acpConversationOwnerRecord) error {
	body, err := json.Marshal(record.Owner)
	if err != nil {
		return err
	}
	email, _ := session.NormalizeEmail(record.Owner.User)
	if _, err := decodeACPAccountOwner(string(body), email); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO acp_session_owners(node_id,key,folder_id,harness,started_by,owner_email)
        VALUES (`+state.LocalNodeSQL+`,?,?,?,?,?)`, key, record.FolderID, record.Harness, string(body), email); err != nil {
		return err
	}
	var existing, existingEmail string
	if err := tx.QueryRowContext(ctx, "SELECT started_by,owner_email FROM acp_session_owners WHERE key=? AND node_id="+state.LocalNodeSQL, key).Scan(&existing, &existingEmail); err != nil {
		return err
	}
	owner, err := decodeACPAccountOwner(existing, existingEmail)
	if err != nil {
		return err
	}
	if !record.Owner.CanWriteSession(session.SessionInfo{StartedBy: &owner}) {
		return ErrACPResumeDenied
	}
	return nil
}

func RememberACPConversationOwner(ctx context.Context, store *state.Store, info session.SessionInfo, fallbackDirectory string) error {
	if info.Transport != "acp" || info.AgentSessionID == "" || info.StartedBy == nil {
		return nil
	}
	directory := info.Directory
	if directory == "" {
		directory = fallbackDirectory
	}
	folderID, err := FolderIdentity(directory)
	if err != nil {
		return err
	}
	if info.FolderID != "" && !SameFolder(info.FolderID, folderID) {
		return errors.New("ACP workspace changed before owner record was saved")
	}
	key, canonical, err := acpOwnerKey(folderID, info.Harness, info.AgentSessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(info.StartedBy)
	if err != nil {
		return err
	}
	email, _ := session.NormalizeEmail(info.StartedBy.User)
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		owner, found, err := loadACPConversationOwner(ctx, tx, folderID, info.Harness, info.AgentSessionID)
		if err != nil {
			return err
		}
		if found && !info.StartedBy.CanWriteSession(session.SessionInfo{StartedBy: &owner}) {
			return ErrACPResumeDenied
		}
		if !found {
			owner = *info.StartedBy
		}
		if err := insertOwnerBinding(ctx, tx, key, acpConversationOwnerRecord{FolderID: folderID, Harness: canonical, Owner: owner}); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET agent_session_id=?,folder_id=?,started_by=?,owner_email=? WHERE id=? AND node_id=`+state.LocalNodeSQL, info.AgentSessionID, folderID, string(body), email, info.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count != 1 {
			return session.ErrSessionNotFound
		}
		return err
	})
}

func ArchivedNativeIDs(ctx context.Context, store *state.Store, folderID, name string) (map[string]bool, error) {
	target, err := HarnessTarget(name)
	if err != nil {
		return nil, err
	}
	rows, err := store.Reader.QueryContext(ctx, `SELECT harness,agent_session_id FROM sessions WHERE node_id=`+state.LocalNodeSQL+` AND folder_id=? AND archived_at>0 AND agent_session_id<>''`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var harness, id string
		if err := rows.Scan(&harness, &id); err != nil {
			return nil, err
		}
		canonical, err := HarnessTarget(harness)
		if err != nil {
			return nil, err
		}
		if canonical == target {
			ids[id] = true
		}
	}
	return ids, rows.Err()
}
