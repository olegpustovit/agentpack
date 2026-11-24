package paths

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestMacOSProtectedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS path syntax")
	}
	const home = "/Users/owner"
	for _, test := range []struct {
		path string
		want bool
	}{
		{home + "/Documents/project", true},
		{home + "/desktop", true},
		{home + "/Downloads", true},
		{home + "/Pictures", true},
		{home + "/Movies", true},
		{home + "/Music", true},
		{home + "/Library/Mobile Documents/project", true},
		{home + "/Library/CloudStorage/provider/project", true},
		{home + "/Library/Group Containers/app", true},
		{home + "/Library/Application Support/AddressBook", true},
		{home + "/Library/Application Support/Cursor", false},
		{home + "/Documents-old", false},
		{"/Volumes/external/project", true},
		{"/System/Volumes/Data/Users/Owner/DOCUMENTS/project", true},
		{"/Users/someone/Desktop/project", true},
		{"/saved/archive.photoslibrary/database", true},
		{"/RealHome/Downloads/project", true},
		{"/code/alias/project", true},
		{"/code/second-alias/../project", true},
		{"/code/safe", false},
	} {
		t.Run(test.path, func(t *testing.T) {
			readlink := func(path string) (string, error) {
				switch path {
				case home:
					return "/RealHome", nil
				case "/code/alias":
					return home + "/Documents", nil
				case "/code/second-alias":
					return "alias", nil
				}
				for _, protected := range []string{"/documents", "/downloads", "/volumes/external", ".photoslibrary"} {
					if strings.Contains(strings.ToLower(path), protected) && !strings.HasSuffix(path, "/Documents-old") {
						t.Fatalf("checked a protected component: %s", path)
					}
				}
				return "", syscall.EINVAL
			}
			if got := macOSProtected(test.path, home, readlink); got != test.want {
				t.Fatalf("protected = %t, want %t", got, test.want)
			}
		})
	}
}
