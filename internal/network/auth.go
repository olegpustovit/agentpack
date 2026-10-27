package network

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type gatewayAuth struct {
	mu       sync.Mutex
	home     string
	password []byte
	setup    string
	sessions map[[32]byte]time.Time
	attempts int
	reset    time.Time
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func newGatewayAuth(home string) (*gatewayAuth, error) {
	directory := filepath.Join(home, "network")
	if err := EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	a := &gatewayAuth{home: directory, sessions: make(map[[32]byte]time.Time)}
	data, err := os.ReadFile(filepath.Join(directory, "root-password"))
	if err == nil {
		if _, err = bcrypt.Cost(data); err != nil {
			return nil, errors.New("invalid gateway password record")
		}
		a.password = data
		return a, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	token, err := os.ReadFile(filepath.Join(directory, "setup-token"))
	if os.IsNotExist(err) {
		token = []byte(randomToken())
		err = os.WriteFile(filepath.Join(directory, "setup-token"), token, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(token) != 64 {
		return nil, errors.New("invalid gateway setup token")
	}
	a.setup = string(token)
	return a, nil
}

func (a *gatewayAuth) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie("agentpack_session")
	if err != nil {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, ok := a.sessions[key]
	if !ok || time.Now().After(expires) {
		delete(a.sessions, key)
		return false
	}
	return true
}

func (a *gatewayAuth) status(w http.ResponseWriter, r *http.Request) {
	authenticated := a.authenticated(r)
	a.mu.Lock()
	setup := len(a.password) == 0
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"setup_required": setup, "authenticated": authenticated})
}

func (a *gatewayAuth) login(w http.ResponseWriter, r *http.Request, setup bool, secure bool) {
	var input struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().After(a.reset) {
		a.attempts = 0
		a.reset = time.Now().Add(time.Minute)
	}
	if a.attempts >= 10 {
		http.Error(w, "try again in a minute", 429)
		return
	}
	a.attempts++
	if setup {
		if len(a.password) > 0 {
			http.Error(w, "administrator already configured", 409)
			return
		}
		if subtle.ConstantTimeCompare([]byte(input.Token), []byte(a.setup)) != 1 {
			http.Error(w, "invalid setup token", 401)
			return
		}
		if len(input.Password) < 12 || len(input.Password) > 72 {
			http.Error(w, "password must contain 12 to 72 bytes", 400)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "could not create password", 500)
			return
		}
		file, err := os.OpenFile(filepath.Join(a.home, "root-password"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			http.Error(w, "could not save administrator", 500)
			return
		}
		_, writeErr := file.Write(hash)
		err = errors.Join(writeErr, file.Close())
		if err != nil {
			http.Error(w, "could not save administrator", 500)
			return
		}
		a.password = hash
		a.setup = ""
		_ = os.Remove(filepath.Join(a.home, "setup-token"))
	} else if len(input.Password) > 72 || bcrypt.CompareHashAndPassword(a.password, []byte(input.Password)) != nil {
		http.Error(w, "invalid password", 401)
		return
	}
	token := randomToken()
	expires := time.Now().Add(12 * time.Hour)
	for key, expiry := range a.sessions {
		if time.Now().After(expiry) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 128 {
		http.Error(w, "too many active logins", 429)
		return
	}
	a.sessions[sha256.Sum256([]byte(token))] = expires
	http.SetCookie(w, &http.Cookie{Name: "agentpack_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, Expires: expires})
	writeJSON(w, map[string]bool{"authenticated": true})
}

func (a *gatewayAuth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("agentpack_session"); err == nil {
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(c.Value)))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "agentpack_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "expected one JSON object", 400)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
