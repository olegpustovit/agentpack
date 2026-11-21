package network

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

var (
	errACPResumeDenied       = errors.New("read-only actor cannot use this ACP conversation")
	errACPResumeOwnerStorage = errors.New("ACP session owner record is unavailable")
)

type acpConversationOwnerRecord struct {
	FolderID string `json:"folder_id"`
	Harness  string `json:"harness"`
	Owner    Actor  `json:"owner"`
}

func acpOwnerKey(folderID, harness, sessionID string) (key, canonical string, err error) {
	if folderID == "" || sessionID == "" {
		return "", "", errors.New("ACP owner key is incomplete")
	}
	target, err := harnessTarget(harness)
	if err != nil {
		return "", "", err
	}
	canonical = string(target)
	digest := sha256.Sum256([]byte(folderID + "\x00" + canonical + "\x00" + sessionID))
	return hex.EncodeToString(digest[:]), canonical, nil
}

func ownerBindingID(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var site []byte
	if err := tx.QueryRowContext(ctx, "SELECT crsql_site_id()").Scan(&site); err != nil {
		return "", err
	}
	return "ownership-" + hex.EncodeToString(site) + "-" + key, nil
}

func decodeACPAccountOwner(body, email string) (Actor, error) {
	var owner Actor
	if err := json.Unmarshal([]byte(body), &owner); err != nil {
		return owner, errors.Join(errACPResumeOwnerStorage, err)
	}
	if owner.User == "" || owner.Node == "" && owner.StableNodeID == "" {
		return owner, errACPResumeOwnerStorage
	}
	if email != "" {
		normalized, err := normalizeEmail(owner.User)
		if err != nil || normalized != email {
			return owner, errACPResumeOwnerStorage
		}
	}
	return owner, nil
}

func loadACPConversationOwner(ctx context.Context, tx *sql.Tx, folderID, harness, sessionID string) (Actor, bool, error) {
	key, canonical, err := acpOwnerKey(folderID, harness, sessionID)
	if err != nil {
		return Actor{}, false, err
	}
	id, err := ownerBindingID(ctx, tx, key)
	if err != nil {
		return Actor{}, false, err
	}
	pendingID, err := ownerBindingID(ctx, tx, "pending")
	if err != nil {
		return Actor{}, false, err
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL+")", pendingID).Scan(&pending); err != nil {
		return Actor{}, false, err
	}
	if pending {
		return Actor{}, false, errACPResumeOwnerStorage
	}
	var body, email, storedFolder, storedHarness string
	var owner Actor
	found := false
	err = tx.QueryRowContext(ctx, "SELECT started_by,owner_email,folder_id,harness FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL, id).Scan(&body, &email, &storedFolder, &storedHarness)
	if err == nil {
		if storedFolder != folderID || storedHarness != canonical {
			return Actor{}, false, errACPResumeOwnerStorage
		}
		owner, err = decodeACPAccountOwner(body, email)
		if err != nil {
			return Actor{}, false, err
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Actor{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT harness,started_by,owner_email,archived_at FROM sessions
        WHERE node_id=`+state.LocalNodeSQL+` AND folder_id=? AND agent_session_id=? AND transport='acp' ORDER BY started_at,id`, folderID, sessionID)
	if err != nil {
		return Actor{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, body, email string
		var archivedAt int64
		if err := rows.Scan(&name, &body, &email, &archivedAt); err != nil {
			return Actor{}, false, err
		}
		target, err := harnessTarget(name)
		if err != nil || string(target) != canonical {
			continue
		}
		if archivedAt != 0 {
			return Actor{}, false, errSessionArchived
		}
		if body == "" || body == "null" {
			continue
		}
		candidate, err := decodeACPAccountOwner(body, email)
		if err != nil {
			return Actor{}, false, err
		}
		if found && !candidate.canWriteSession(SessionInfo{StartedBy: &owner}) {
			return Actor{}, false, errACPResumeOwnerStorage
		}
		if !found {
			owner, found = candidate, true
		}
	}
	return owner, found, rows.Err()
}

func readACPConversationOwner(ctx context.Context, store *state.Store, folderID, harness, sessionID string) (Actor, bool, error) {
	tx, err := store.Reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Actor{}, false, errors.Join(errACPResumeOwnerStorage, err)
	}
	defer tx.Rollback()
	owner, found, err := loadACPConversationOwner(ctx, tx, folderID, harness, sessionID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		err = errors.Join(errACPResumeOwnerStorage, err)
	}
	return owner, found, err
}

func authorizeACPResume(ctx context.Context, store *state.Store, actor Actor, session SessionInfo) error {
	if session.AgentSessionID == "" {
		return nil
	}
	target, err := harnessTarget(session.Harness)
	if err != nil {
		return err
	}
	rows, err := store.Reader.QueryContext(ctx, "SELECT harness FROM sessions WHERE agent_session_id=? AND archived_at>0", session.AgentSessionID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		canonical, err := harnessTarget(name)
		if err == nil && canonical == target {
			rows.Close()
			return errSessionArchived
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || session.Transport != "acp" {
		return err
	}
	folderID, err := FolderIdentity(session.Directory)
	if err != nil {
		return err
	}
	owner, found, err := readACPConversationOwner(ctx, store, folderID, session.Harness, session.AgentSessionID)
	if err != nil {
		return err
	}
	if !found {
		if actor.Kind == "peer" {
			return errACPResumeDenied
		}
		return nil
	}
	if !actor.canWriteSession(SessionInfo{StartedBy: &owner}) {
		return errACPResumeDenied
	}
	return nil
}

// Hidden binding rows keep immutable native ownership even after legacy worker directories vanished.
func insertOwnerBinding(ctx context.Context, tx *sql.Tx, key string, record acpConversationOwnerRecord) error {
	id, err := ownerBindingID(ctx, tx, key)
	if err != nil {
		return err
	}
	body, err := json.Marshal(record.Owner)
	if err != nil {
		return err
	}
	email, _ := normalizeEmail(record.Owner.User)
	if _, err := decodeACPAccountOwner(string(body), email); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO sessions(id,node_id,folder_id,harness,started_by,owner_email,hidden,ended_at)
        VALUES (?,`+state.LocalNodeSQL+`,?,?,?,?,1,?)`, id, record.FolderID, record.Harness, string(body), email, time.Now().UnixNano())
	if err != nil {
		return err
	}
	// Only an import marker has an empty scope and owner; valid bindings stay immutable.
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET folder_id=?,harness=?,started_by=?,owner_email=?
		WHERE id=? AND node_id=`+state.LocalNodeSQL+` AND folder_id='' AND harness='' AND started_by='' AND owner_email=''`,
		record.FolderID, record.Harness, string(body), email, id); err != nil {
		return err
	}
	var existing, existingEmail string
	if err := tx.QueryRowContext(ctx, "SELECT started_by,owner_email FROM sessions WHERE id=? AND node_id="+state.LocalNodeSQL, id).Scan(&existing, &existingEmail); err != nil {
		return err
	}
	owner, err := decodeACPAccountOwner(existing, existingEmail)
	if err != nil {
		return err
	}
	if !record.Owner.canWriteSession(SessionInfo{StartedBy: &owner}) {
		return errACPResumeDenied
	}
	return nil
}

func rememberACPConversationOwner(ctx context.Context, store *state.Store, session SessionInfo, fallbackDirectory string) error {
	if session.Transport != "acp" || session.AgentSessionID == "" || session.StartedBy == nil {
		return nil
	}
	directory := session.Directory
	if directory == "" {
		directory = fallbackDirectory
	}
	folderID, err := FolderIdentity(directory)
	if err != nil {
		return err
	}
	if session.FolderID != "" && !SameFolder(session.FolderID, folderID) {
		return errors.New("ACP workspace changed before owner record was saved")
	}
	key, canonical, err := acpOwnerKey(folderID, session.Harness, session.AgentSessionID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(session.StartedBy)
	if err != nil {
		return err
	}
	email, _ := normalizeEmail(session.StartedBy.User)
	return state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		owner, found, err := loadACPConversationOwner(ctx, tx, folderID, session.Harness, session.AgentSessionID)
		if err != nil {
			return err
		}
		if found && !session.StartedBy.canWriteSession(SessionInfo{StartedBy: &owner}) {
			return errACPResumeDenied
		}
		if !found {
			owner = *session.StartedBy
		}
		if err := insertOwnerBinding(ctx, tx, key, acpConversationOwnerRecord{FolderID: folderID, Harness: canonical, Owner: owner}); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET agent_session_id=?,folder_id=?,started_by=?,owner_email=? WHERE id=? AND node_id=`+state.LocalNodeSQL, session.AgentSessionID, folderID, string(body), email, session.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count != 1 {
			return errSessionNotFound
		}
		return err
	})
}

func archivedNativeIDs(ctx context.Context, store *state.Store, folderID, name string) (map[string]bool, error) {
	target, err := harnessTarget(name)
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
		canonical, err := harnessTarget(harness)
		if err != nil {
			return nil, err
		}
		if canonical == target {
			ids[id] = true
		}
	}
	return ids, rows.Err()
}
