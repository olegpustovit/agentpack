package codex

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unicode/utf16"

	"github.com/danieljoos/wincred"
	keyring "github.com/zalando/go-keyring"
)

func readCodexKeyring(service, account string) (string, error) {
	credential, err := wincred.GetGenericCredential(codexKeyringTarget(service, account))
	if err == nil {
		defer clear(credential.CredentialBlob)
		return decodeCodexWindowsPassword(credential.CredentialBlob)
	}
	if err != syscall.ERROR_NOT_FOUND {
		return "", err
	}
	return keyring.Get(service, account)
}

func deleteCodexKeyring(service, account string) error {
	credential, err := wincred.GetGenericCredential(codexKeyringTarget(service, account))
	if err == nil {
		err = credential.Delete()
	}
	if err != nil && err != syscall.ERROR_NOT_FOUND {
		return err
	}
	if err := keyring.Delete(service, account); err != nil && err != keyring.ErrNotFound {
		return err
	}
	return nil
}

func codexKeyringTarget(service, account string) string {
	return account + "." + service
}

func decodeCodexWindowsPassword(blob []byte) (string, error) {
	if len(blob)%2 != 0 {
		return "", fmt.Errorf("invalid Codex Windows keyring password length")
	}
	units := make([]uint16, len(blob)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(blob[index*2:])
	}
	return string(utf16.Decode(units)), nil
}
