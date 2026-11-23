package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state"
	"golang.org/x/crypto/bcrypt"
)

const browserSessionLifetime = 30 * 24 * time.Hour

// browserSessionSlide is how stale a session expiry may get before a request extends it.
const browserSessionSlide = time.Hour

type gatewayAuth struct {
	mu        sync.Mutex
	store     *state.Store
	home      string
	now       func() time.Time
	telemetry map[[32]byte]telemetryWindow
	attempts  network.ClientWindow
	failures  map[string]loginFailure
}

func newGatewayAuth(home string, store *state.Store) (*gatewayAuth, error) {
	if store == nil {
		return nil, errors.New("gateway state unavailable")
	}
	directory := filepath.Join(home, "network")
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	a := &gatewayAuth{store: store, home: directory, now: time.Now, failures: map[string]loginFailure{}}
	claimed, err := a.claimed(context.Background())
	if err != nil {
		return nil, err
	}
	if !claimed {
		if _, err := a.legacyHash(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *gatewayAuth) claimed(ctx context.Context) (bool, error) {
	var claimed bool
	err := a.store.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users) OR (SELECT join_peer<>'' FROM local_node WHERE id=1)").Scan(&claimed)
	return claimed, err
}

func (a *gatewayAuth) legacyHash() ([]byte, error) {
	path := filepath.Join(a.home, "root-password")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return nil, errors.New("invalid legacy password record")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := bcrypt.Cost(data); err != nil {
		return nil, errors.New("invalid legacy password record")
	}
	return data, nil
}

func cookieName(r *http.Request) string {
	if secureRequest(r) {
		return "__Host-agentpack_session"
	}
	return "agentpack_session"
}

func (a *gatewayAuth) email(r *http.Request) string {
	cookie, err := r.Cookie(cookieName(r))
	if err != nil || len(cookie.Value) == 0 || len(cookie.Value) > 256 {
		return ""
	}
	key := sha256.Sum256([]byte(cookie.Value))
	now := a.now()
	var email string
	var expires int64
	err = a.store.Reader.QueryRowContext(r.Context(), `SELECT s.email,s.expires_at FROM auth_sessions s JOIN users u ON u.email=s.email
WHERE s.token_hash=? AND s.expires_at>? AND u.disabled=0 AND s.created_at>=u.sessions_valid_after AND s.session_epoch=u.sessions_valid_after`, key[:], now.UnixMilli()).Scan(&email, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	// Sliding expiry moves at most hourly: a write per request contended with streaming for the one write lock.
	if err == nil && expires >= now.Add(browserSessionLifetime-browserSessionSlide).UnixMilli() {
		return email
	}
	if err == nil {
		var result sql.Result
		result, err = a.store.DB.ExecContext(r.Context(), `UPDATE auth_sessions SET expires_at=max(expires_at,?) WHERE token_hash=?
AND EXISTS(SELECT 1 FROM users u WHERE u.email=auth_sessions.email AND u.disabled=0 AND auth_sessions.created_at>=u.sessions_valid_after AND auth_sessions.session_epoch=u.sessions_valid_after)`, now.Add(browserSessionLifetime).UnixMilli(), key[:])
		if err == nil {
			n, e := result.RowsAffected()
			if e == nil && n == 1 {
				return email
			}
			err = e
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		if event := obs.From(r.Context()); event != nil {
			event.Fail(err)
		}
	}
	return ""
}

func (a *gatewayAuth) authenticated(r *http.Request) bool { return a.email(r) != "" }

func (a *gatewayAuth) status(w http.ResponseWriter, r *http.Request) {
	claimed, err := a.claimed(r.Context())
	if err != nil {
		authFailure(w, r, "account state unavailable", err)
		return
	}
	legacy := false
	if !claimed {
		hash, err := a.legacyHash()
		if err != nil {
			authFailure(w, r, "legacy account unavailable", err)
			return
		}
		legacy = len(hash) != 0
	}
	var joining bool
	if err := a.store.Reader.QueryRowContext(r.Context(), "SELECT NOT EXISTS(SELECT 1 FROM users) AND join_peer<>'' FROM local_node WHERE id=1").Scan(&joining); err != nil {
		authFailure(w, r, "account state unavailable", err)
		return
	}
	email := a.email(r)
	network.WriteJSON(w, AccountStatus{Joining: joining, Claimed: claimed, Legacy: legacy, Authenticated: email != "", Email: email})
}

func (a *gatewayAuth) claim(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email           string `json:"email"`
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password,omitempty"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	email, err := session.NormalizeEmail(input.Email)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !a.allowLogin(w, r, email) {
		return
	}
	claimed, err := a.claimed(r.Context())
	if err != nil {
		authFailure(w, r, "account state unavailable", err)
		return
	}
	if claimed {
		http.Error(w, "gateway already claimed", 409)
		return
	}
	hash, err := a.legacyHash()
	if err != nil {
		authFailure(w, r, "legacy account unavailable", err)
		return
	}
	if len(hash) != 0 {
		candidate := input.CurrentPassword
		if len(candidate) > 72 {
			candidate = candidate[:72]
		}
		compared := bcrypt.CompareHashAndPassword(hash, []byte(candidate))
		if input.CurrentPassword == "" || len(input.CurrentPassword) > 72 || compared != nil {
			a.failedLogin(r, email)
			http.Error(w, "invalid current password", 401)
			return
		}
	} else {
		hash, err = passwordHash(input.Password)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	won, err := claimOwner(r.Context(), a.store, email, hash)
	if err != nil {
		authFailure(w, r, "could not claim gateway", err)
		return
	}
	if !won {
		http.Error(w, "gateway already claimed", 409)
		return
	}
	for _, name := range []string{"root-password", "setup-token"} {
		if err := os.Remove(filepath.Join(a.home, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			authFailure(w, r, "account created; legacy file cleanup failed", err)
			return
		}
	}
	user := User{Email: email}
	if err := a.store.Reader.QueryRowContext(r.Context(), "SELECT sessions_valid_after FROM users WHERE email=?", email).Scan(&user.SessionsValidAfter); err != nil {
		authFailure(w, r, "account state unavailable", err)
		return
	}
	a.issueSession(w, r, user)
}

func (a *gatewayAuth) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !network.ReadJSON(w, r, &input) {
		return
	}
	email, _ := session.NormalizeEmail(input.Email)
	if !a.allowLogin(w, r, email) {
		return
	}
	user, err := verifyLogin(r.Context(), a.store, input.Email, input.Password)
	if errors.Is(err, errInvalidLogin) {
		a.failedLogin(r, email)
		http.Error(w, errInvalidLogin.Error(), 401)
		return
	}
	if err != nil {
		authFailure(w, r, "login unavailable", err)
		return
	}
	a.issueSession(w, r, user)
}

func (a *gatewayAuth) issueSession(w http.ResponseWriter, r *http.Request, user User) {
	token := network.RandomToken()
	key := sha256.Sum256([]byte(token))
	now := a.now()
	expires := now.Add(browserSessionLifetime)
	full := false
	err := state.Transaction(r.Context(), a.store.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM auth_sessions WHERE expires_at<=?", now.UnixMilli()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM auth_sessions").Scan(&count); err != nil {
			return err
		}
		if count >= 128 {
			full = true
			return nil
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO auth_sessions(token_hash,email,created_at,expires_at,session_epoch)
SELECT ?,email,max(?,sessions_valid_after),?,sessions_valid_after FROM users WHERE email=? AND disabled=0 AND sessions_valid_after=?`, key[:], now.UnixMilli(), expires.UnixMilli(), user.Email, user.SessionsValidAfter)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return errInvalidLogin
		}
		return err
	})
	if errors.Is(err, errInvalidLogin) {
		http.Error(w, errInvalidLogin.Error(), 401)
		return
	}
	if err != nil {
		authFailure(w, r, "could not save login", err)
		return
	}
	if full {
		http.Error(w, "too many active logins", 429)
		return
	}
	a.mu.Lock()
	delete(a.failures, "email:"+user.Email)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, Expires: expires})
	network.WriteJSON(w, map[string]any{"authenticated": true, "email": user.Email})
}

func (g *Gateway) logout(w http.ResponseWriter, r *http.Request) {
	if g.auth.logout(w, r) {
		g.emitEvent(eventMessage{Topic: "auth"})
	}
}

func (a *gatewayAuth) logout(w http.ResponseWriter, r *http.Request) bool {
	if cookie, err := r.Cookie(cookieName(r)); err == nil {
		hash := sha256.Sum256([]byte(cookie.Value))
		if _, err := a.store.DB.ExecContext(r.Context(), "DELETE FROM auth_sessions WHERE token_hash=?", hash[:]); err != nil {
			authFailure(w, r, "could not sign out", err)
			return false
		}
	}
	clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (a *gatewayAuth) logoutAll(w http.ResponseWriter, r *http.Request) {
	email := a.email(r)
	if email == "" {
		http.Error(w, "login required", 401)
		return
	}
	err := state.Transaction(r.Context(), a.store.DB, func(tx *sql.Tx) error { return revokeUserSessions(r.Context(), tx, email, a.now()) })
	if err != nil {
		authFailure(w, r, "could not sign out everywhere", err)
		return
	}
	clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
}

func authFailure(w http.ResponseWriter, r *http.Request, message string, err error) {
	if event := obs.From(r.Context()); event != nil {
		event.Fail(fmt.Errorf("%s: %w", message, err))
	}
	http.Error(w, message, http.StatusInternalServerError)
}
