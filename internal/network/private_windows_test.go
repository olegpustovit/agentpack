//go:build windows

package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		sddl := security.String()
		if !strings.Contains(sddl, user.User.Sid.String()) || strings.Count(sddl, "(A;") != 1 {
			t.Fatalf("credential ACL grants more than current user: %s", sddl)
		}
	}
}
