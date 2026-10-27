//go:build windows

package network

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPrivateDirectoryProtectsInheritedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(path, "credential.json")
	if err := os.WriteFile(credential, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, credential} {
		security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := security.DACL()
		if err != nil || dacl == nil || dacl.AceCount != 1 {
			t.Fatalf("credential ACL must contain one user grant: %s (%v)", security.String(), err)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
			t.Fatalf("credential ACL grants more than current user: %s", security.String())
		}
	}
}
