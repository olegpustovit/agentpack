//go:build windows

package paths

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// EnsurePrivateDirectory protects credentials with an inheritable current-user
// DACL; Windows ignores the Unix mode supplied to MkdirAll and WriteFile.
func EnsurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	security, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := security.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect private directory: %w", err)
	}
	return nil
}

func checkStagingOwnership(path string, _ os.FileInfo, existing bool) error {
	flags := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION)
	if existing {
		flags |= windows.DACL_SECURITY_INFORMATION
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !owner.Equals(user.User.Sid) {
		return fmt.Errorf("staging root must be owned by the current user: %s", path)
	}
	if !existing {
		return nil
	}
	control, _, err := security.Control()
	if err != nil {
		return err
	}
	dacl, _, err := security.DACL()
	if err != nil {
		return err
	}
	// Earlier releases left the inherited ACL on a root the user owns; make it private instead of refusing it.
	if control&windows.SE_DACL_PROTECTED == 0 || dacl == nil || dacl.AceCount != 1 {
		return EnsurePrivateDirectory(path)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
		return EnsurePrivateDirectory(path)
	}
	return nil
}
