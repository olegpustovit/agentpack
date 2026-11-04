package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
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

// Each private record binds a native session ID to the actor that first opened it.
func acpConversationOwnerPath(home, folderID, harness, sessionID string) (directory, path, canonicalHarness string, err error) {
	if home == "" || folderID == "" || sessionID == "" {
		return "", "", "", errors.New("ACP owner key is incomplete")
	}
	target, err := harnessTarget(harness)
	if err != nil {
		return "", "", "", err
	}
	canonicalHarness = string(target)
	key := sha256.Sum256([]byte(folderID + "\x00" + canonicalHarness + "\x00" + sessionID))
	directory = filepath.Join(home, "network", "acp-session-owners")
	path = filepath.Join(directory, hex.EncodeToString(key[:])+".json")
	return directory, path, canonicalHarness, nil
}

func readACPConversationOwner(home, folderID, harness, sessionID string) (Actor, bool, error) {
	_, path, canonicalHarness, err := acpConversationOwnerPath(home, folderID, harness, sessionID)
	if err != nil {
		return Actor{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Actor{}, false, nil
	}
	if err != nil {
		return Actor{}, false, errors.Join(errACPResumeOwnerStorage, err)
	}
	var record acpConversationOwnerRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return Actor{}, false, errors.Join(errACPResumeOwnerStorage, err)
	}
	if record.FolderID != folderID || record.Harness != canonicalHarness || (record.Owner.Node == "" && record.Owner.StableNodeID == "") {
		return Actor{}, false, errors.Join(errACPResumeOwnerStorage, errors.New("invalid owner record"))
	}
	return record.Owner, true, nil
}

func authorizeACPResume(home string, actor Actor, session SessionInfo) error {
	if session.Transport != "acp" || session.AgentSessionID == "" {
		return nil
	}
	folderID, err := FolderIdentity(session.Directory)
	if err != nil {
		return err
	}
	owner, found, err := readACPConversationOwner(home, folderID, session.Harness, session.AgentSessionID)
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

func rememberACPConversationOwner(home string, session SessionInfo, fallbackDirectory string) error {
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
	if session.FolderID != "" && session.FolderID != folderID {
		return errors.New("ACP workspace changed before owner record was saved")
	}
	ownerDir, path, canonicalHarness, err := acpConversationOwnerPath(home, folderID, session.Harness, session.AgentSessionID)
	if err != nil {
		return err
	}
	if err := EnsurePrivateDirectory(ownerDir); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(filepath.Dir(ownerDir), "acp-session-owners.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	if owner, found, err := readACPConversationOwner(home, folderID, canonicalHarness, session.AgentSessionID); err != nil {
		return err
	} else if found {
		if session.StartedBy.canWriteSession(SessionInfo{StartedBy: &owner}) {
			return nil
		}
		return errACPResumeDenied
	}
	record := acpConversationOwnerRecord{FolderID: folderID, Harness: canonicalHarness, Owner: *session.StartedBy}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(ownerDir, ".owner-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return os.Rename(temporary, path)
}
