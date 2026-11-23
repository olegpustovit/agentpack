package network

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

// RegisterPresence advertises a native-stdio launch without changing its pipes.
// Its process is owned by the caller; the node only receives public metadata.
func RegisterPresence(home, version string, info SessionInfo, process *os.Process) (func(), error) {
	root := filepath.Join(home, "network", "sessions")
	if err := paths.EnsurePrivateDirectory(root); err != nil {
		return nil, err
	}
	directory, err := createSessionDirectory(root, "")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	record := WorkerRecord{ID: filepath.Base(directory), PID: os.Getpid(), Address: listener.Addr().String(), Secret: randomToken()}
	data, err := json.Marshal(record)
	if err != nil {
		listener.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		listener.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, TerminalState{Running: true, ExitCode: -1}) })
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		if err := process.Kill(); err != nil {
			http.Error(w, "could not stop process", 409)
			return
		}
		writeJSON(w, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("GET /attach", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this launch uses native stdio; browser input is unavailable", 409)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+record.Secret)) != 1 {
			http.Error(w, "worker authentication required", 401)
			return
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go server.Serve(listener)
	path := "/api/local/sessions/" + record.ID + "/presence"
	data, err = json.Marshal(info)
	if err != nil {
		server.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	if _, err := NodeRequest(context.Background(), home, http.MethodPost, path, bytes.NewReader(data)); err != nil {
		server.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			server.Close()
			if _, err := NodeRequest(context.Background(), home, http.MethodDelete, path); err != nil {
				if spool, spoolErr := obs.OpenSpool(home, obs.SpoolOptions{Proc: "cli"}); spoolErr == nil {
					ctx := obs.WithSink(obs.ProcessTrace(context.Background()), spool, obs.BuildMetadata(version, localStateNode, "cli"))
					_, event := obs.Start(ctx, "session", "presence_end")
					event.Set("session_id", record.ID)
					event.Fail(err)
					event.End()
					_ = spool.Close()
				}
				fmt.Fprintln(os.Stderr, "warning: session end could not be persisted")
			}
			os.RemoveAll(directory)
		})
	}, nil
}

// Native-stdio callers use this endpoint so foreground setup never opens state.db.
func (g *Gateway) servePresence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, event := obs.Start(r.Context(), "session", "presence")
	event.Set("session_id", id)
	if actor, ok := actorFrom(r.Context()); ok {
		event.Set("user", actor.User)
	}
	defer event.End()
	if r.Method == http.MethodDelete {
		if err := endSession(r.Context(), g.store.DB, id, TerminalState{ExitCode: -1}); err != nil {
			event.Fail(err)
			http.Error(w, "could not persist session end", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ended": true})
		return
	}
	record, err := workerRecord(g.options.Home, id)
	if err != nil {
		event.Fail(err)
		http.Error(w, "session unavailable", http.StatusNotFound)
		return
	}
	var info SessionInfo
	if !readJSON(w, r, &info) {
		return
	}
	info.FolderID, err = FolderIdentity(info.Directory)
	if err != nil {
		fail(w, r, err)
		return
	}
	info.ID = record.ID
	info.LastUsed = time.Now().UTC()
	if info.StartedAt.IsZero() {
		info.StartedAt = info.LastUsed
	}
	actor, ok := actorFrom(r.Context())
	if !ok {
		http.Error(w, "local actor unavailable", http.StatusForbidden)
		return
	}
	info.StartedBy = &actor
	info.State = TerminalState{Running: true, ExitCode: -1}
	if err := upsertSession(r.Context(), g.store.DB, info); err != nil {
		event.Fail(err)
		http.Error(w, "could not register session", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"registered": true})
}
