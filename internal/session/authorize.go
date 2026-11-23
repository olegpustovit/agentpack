package session

import "net/http"

// AuthorizeWrite answers 409 for an archived session and 403 for an actor that may not change
// a chat or shell, and reports whether the request may go on.
func AuthorizeWrite(w http.ResponseWriter, r *http.Request, info SessionInfo) bool {
	if !info.ArchivedAt.IsZero() {
		http.Error(w, "archived", http.StatusConflict)
		return false
	}
	if info.Transport != "acp" && info.Transport != "shell" {
		return true
	}
	actor, ok := ActorFrom(r.Context())
	if !ok {
		actor = BrowserActor("")
	}
	if actor.CanWriteSession(info) {
		return true
	}
	http.Error(w, "read-only actor cannot modify this session", http.StatusForbidden)
	return false
}
