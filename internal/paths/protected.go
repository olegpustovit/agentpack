package paths

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// LookPathForBackground avoids statting protected executables, including links
// found through PATH. Use the platform's executable check on each safe candidate.
func LookPathForBackground(program string) (string, error) {
	if runtime.GOOS != "darwin" {
		return exec.LookPath(program)
	}
	if filepath.IsAbs(program) {
		if !RequiresUserAccess(program) {
			return exec.LookPath(program)
		}
		return "", exec.ErrNotFound
	}
	if filepath.Base(program) == program {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if !filepath.IsAbs(dir) {
				continue
			}
			candidate := filepath.Join(dir, program)
			if !RequiresUserAccess(candidate) {
				if path, err := exec.LookPath(candidate); err == nil {
					return path, nil
				}
			}
		}
	}
	return "", exec.ErrNotFound
}

// RequiresUserAccess reports whether an unsolicited read could trigger macOS privacy consent.
// Check it before statting, opening, or giving an automatically selected path to a subprocess.
func RequiresUserAccess(path string) bool {
	if runtime.GOOS != "darwin" || !filepath.IsAbs(path) {
		return false
	}
	home, _ := os.UserHomeDir()
	return macOSProtected(path, home, os.Readlink)
}

func macOSProtected(path, home string, readlink func(string) (string, error)) bool {
	homes := []string{macOSVolumePath(home)}
	protected := func(path string) bool {
		path = macOSVolumePath(path)
		switch filepath.Ext(path) {
		case ".photoslibrary", ".photolibrary", ".musiclibrary", ".tvlibrary", ".imovielibrary":
			return true
		}
		for _, root := range []string{"/volumes", "/network"} {
			if path == root || strings.HasPrefix(path, root+"/") {
				return true
			}
		}
		if users, ok := strings.CutPrefix(path, "/users/"); ok {
			if _, relative, found := strings.Cut(users, "/"); found && macOSProtectedHomePath(relative) {
				return true
			}
		}
		for _, home := range homes {
			if home != "" && home != "/" {
				if relative, ok := strings.CutPrefix(path, home+"/"); ok && macOSProtectedHomePath(relative) {
					return true
				}
			}
		}
		return false
	}
	if protected(filepath.Clean(path)) {
		return true
	}
	if filepath.IsAbs(home) {
		if real, blocked := resolveBeforeProtected(home, protected, readlink); !blocked && macOSVolumePath(real) != homes[0] {
			homes = append(homes, macOSVolumePath(real))
		}
	}
	_, blocked := resolveBeforeProtected(path, protected, readlink)
	return blocked
}

func macOSVolumePath(path string) string {
	// APFS normally ignores case. The data volume also exposes the same paths behind a firmlink.
	path = strings.ToLower(path)
	if relative, ok := strings.CutPrefix(path, "/system/volumes/data/"); ok {
		return "/" + relative
	}
	return path
}

func macOSProtectedHomePath(relative string) bool {
	first, rest, _ := strings.Cut(relative, "/")
	switch first {
	case "desktop", "documents", "downloads", "pictures", "movies", "music":
		return true
	case "library":
		first, _, _ = strings.Cut(rest, "/")
		switch first {
		case "mobile documents", "cloudstorage", "containers", "group containers", "mail", "messages", "safari",
			"calendars", "reminders", "addressbook", "photos":
			return true
		case "application support":
			return rest == "application support/addressbook" || strings.HasPrefix(rest, "application support/addressbook/")
		}
	}
	return false
}

// EvalSymlinks stats the destination of each link. Stop before even inspecting a protected component.
func resolveBeforeProtected(path string, protected func(string) bool, readlink func(string) (string, error)) (string, bool) {
	dir := "/"
	for links := 0; path != ""; {
		part, remaining, _ := strings.Cut(path, "/")
		path = remaining
		switch part {
		case "", ".":
			continue
		case "..":
			dir = filepath.Dir(dir)
			continue
		}
		next := filepath.Join(dir, part)
		if protected(next) {
			return "", true
		}
		link, err := readlink(next)
		switch {
		case err == nil:
			links++
			if links > 40 {
				return "", true
			}
			if filepath.IsAbs(link) {
				dir = "/"
			}
			path = link + "/" + path
		case errors.Is(err, syscall.EINVAL):
			dir = next
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			return "", false
		default:
			return "", true
		}
	}
	return dir, false
}
