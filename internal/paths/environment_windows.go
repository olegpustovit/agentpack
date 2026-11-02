package paths

import (
	"os"

	"golang.org/x/sys/windows"
)

// RestoreUserEnvironment fills USERPROFILE and LOCALAPPDATA from the Windows
// known-folder API when the launching shell dropped them (some MSYS and
// service launchers do). os.UserHomeDir, agentpack's own data home and every
// harness child process read these variables, so restoring them once at
// startup fixes all of them. Variables that are set are never changed.
func RestoreUserEnvironment() {
	restore("USERPROFILE", windows.FOLDERID_Profile)
	restore("LOCALAPPDATA", windows.FOLDERID_LocalAppData)
}

func restore(name string, folder *windows.KNOWNFOLDERID) {
	if os.Getenv(name) != "" {
		return
	}
	if path, err := windows.KnownFolderPath(folder, windows.KF_FLAG_DEFAULT); err == nil && path != "" {
		_ = os.Setenv(name, path)
	}
}
