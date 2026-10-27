package network

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// RegisterPresence advertises a native-stdio launch without changing its pipes.
// Its process is owned by the caller; the node only receives public metadata.
func RegisterPresence(home string, info SessionInfo, process *os.Process) (func(), error) {
	root := filepath.Join(home, "network", "sessions")
	if err := EnsurePrivateDirectory(root); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	record := WorkerRecord{Session: info, ID: filepath.Base(directory), PID: os.Getpid(), Address: listener.Addr().String(), Secret: randomToken()}
	data, _ := json.Marshal(record)
	if err = os.WriteFile(filepath.Join(directory, "worker.json"), data, 0600); err != nil {
		listener.Close()
		os.RemoveAll(directory)
		return nil, err
	}
	if err = remember(home, info); err != nil {
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
	return func() { server.Close(); os.RemoveAll(directory) }, nil
}
