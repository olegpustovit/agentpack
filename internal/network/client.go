package network

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

type AgentCapabilities struct {
	LoadSession         bool                      `json:"loadSession,omitempty"`
	PromptCapabilities  *AgentPromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities *AgentSessionCapabilities `json:"sessionCapabilities,omitempty"`
}

type AgentPromptCapabilities struct {
	Image bool `json:"image,omitempty"`
}

type AgentSessionCapabilities struct {
	Resume *struct{} `json:"resume,omitempty"`
	Fork   *struct{} `json:"fork,omitempty"`
}

type SessionInfo struct {
	NodeID            string             `json:"node_id,omitempty"`
	Reachable         bool               `json:"reachable"`
	LastSync          time.Time          `json:"last_sync,omitzero"`
	EndedAt           time.Time          `json:"ended_at,omitzero"`
	ArchivedAt        time.Time          `json:"archived_at,omitzero"`
	LastUsed          time.Time          `json:"last_used,omitzero"`
	LastEventAt       time.Time          `json:"last_event_at,omitzero"`
	FolderID          string             `json:"folder_id,omitempty"`
	ID                string             `json:"id"`
	Name              string             `json:"name,omitempty"`
	Title             string             `json:"title,omitempty"`
	Directory         string             `json:"directory"`
	DefinitionRoot    string             `json:"definition_root,omitempty"`
	Harness           string             `json:"harness"`
	Mode              string             `json:"mode"`
	Transport         string             `json:"transport,omitempty"`
	AgentSessionID    string             `json:"agent_session_id,omitempty"`
	AgentCapabilities *AgentCapabilities `json:"agent_capabilities,omitempty"`
	ConversationState string             `json:"conversation_state,omitempty"`
	State             TerminalState      `json:"state"`
	StartedAt         time.Time          `json:"started_at,omitzero"`
	StartedBy         *Actor             `json:"started_by,omitempty"`
	Goal              *Goal              `json:"goal,omitempty"`
	Subagents         []SubagentSummary  `json:"subagents,omitempty"`
	// ParentID is the chat whose agent started this helper session.
	ParentID string `json:"parent_id,omitempty"`
	// Tools lets the chat's agent call the agentpack MCP server.
	Tools bool `json:"tools,omitempty"`
}

func workerRecord(home, id string) (WorkerRecord, error) {
	var record WorkerRecord
	if !validSessionID(id) {
		return record, errors.New("invalid session ID")
	}
	data, err := os.ReadFile(filepath.Join(home, "network", "sessions", id, "worker.json"))
	if err != nil {
		return record, err
	}
	return parseWorkerRecord(data, id)
}

func parseWorkerRecord(data []byte, id string) (WorkerRecord, error) {
	var record WorkerRecord
	var err error
	if err = json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	host, port, err := net.SplitHostPort(record.Address)
	if err != nil || host != "127.0.0.1" || port == "" || record.ID != id || len(record.Secret) != 64 {
		return WorkerRecord{}, errors.New("invalid worker record")
	}
	return record, nil
}

func workerRequestTimeout(method, path string) time.Duration {
	path, _, _ = strings.Cut(path, "?")
	if method == http.MethodGet && strings.HasPrefix(path, "/api/cache/") {
		return 2 * time.Minute
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/api/logs/traces/") {
		return 5 * time.Second
	}
	if strings.HasPrefix(path, "/api/mcp/") {
		// Refreshing may ask Main over the mesh, and starting a login discovers the authorization server.
		return time.Minute
	}
	if method == http.MethodPost &&
		(path == "/stop" || path == "/restart" || path == "/api/sessions" ||
			strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/restart") ||
			strings.HasSuffix(path, "/conversation/prompt") || strings.HasSuffix(path, "/conversation/blobs")) ||
		method == http.MethodGet && strings.Contains(path, "/blobs/") {
		return 2 * time.Minute
	}
	return 2 * time.Second
}

func workerRequest(ctx context.Context, record WorkerRecord, method, path, operationID string, body ...io.Reader) (*http.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = body[0]
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+record.Address+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	if trace := obs.TraceParent(ctx); trace != "" {
		request.Header.Set("traceparent", trace)
	}
	if operationID != "" {
		request.Header.Set("Idempotency-Key", operationID)
	}
	// Writes get a fresh connection: a reused one that drops would be retried silently and hide an ambiguous result.
	transport := workerTransport
	if method != http.MethodGet {
		transport = &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
	}
	client := http.Client{Timeout: workerRequestTimeout(method, path), Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(request)
}

// workerTransport keeps loopback read connections to workers alive, so the 2 s session polls do not reconnect each time.
var workerTransport = &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}

// workerStates asks every listed local worker for its live terminal state at once; a worker that does not answer is left out.
func workerStates(ctx context.Context, home string, sessions []SessionInfo) map[string]TerminalState {
	var mu sync.Mutex
	var wg sync.WaitGroup
	states := map[string]TerminalState{}
	for _, info := range sessions {
		record, err := workerRecord(home, info.ID)
		if err != nil {
			continue
		}
		wg.Go(func() {
			if terminal, err := sessionState(ctx, record); err == nil {
				mu.Lock()
				states[info.ID] = terminal
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return states
}

func Sessions(ctx context.Context, home string) ([]SessionInfo, error) {
	store, err := state.Open(home)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return sessionsIn(ctx, store.Reader, home)
}

// sessionsIn lists this node's live sessions from an open store; the gateway passes its own.
func sessionsIn(ctx context.Context, db *sql.DB, home string) ([]SessionInfo, error) {
	sessions, err := querySessions(ctx, db, "SELECT "+sessionColumns+" FROM sessions WHERE node_id="+state.LocalNodeSQL+" AND ended_at=0 ORDER BY id")
	if err != nil {
		return nil, err
	}
	states := workerStates(ctx, home, sessions)
	result := []SessionInfo{}
	for _, info := range sessions {
		if terminal, ok := states[info.ID]; ok {
			info.State = terminal
			result = append(result, info)
		}
	}
	return result, nil
}

func StartWorker(ctx context.Context, home, executable string, launch WorkerLaunch) (string, error) {
	if launch.Session.StartedAt.IsZero() {
		launch.Session.StartedAt = time.Now()
	}
	directory := filepath.Join(home, "network")
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return "", err
	}
	log, err := os.OpenFile(filepath.Join(directory, "workers.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	command := exec.Command(executable, "session-worker")
	trace := obs.ParseTraceParent(obs.TraceParent(ctx))
	command.Env = append(os.Environ(), "AGENTPACK_TRACE="+trace.ID)
	ConfigureDetached(command)
	// The worker outlives the gateway, so its stderr must be the file itself: a pipe read by this
	// process breaks when the gateway stops and the worker's next log line kills it with SIGPIPE.
	logStart, err := log.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	command.Stderr = log
	input, err := command.StdinPipe()
	if err != nil {
		return "", err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return "", err
	}
	if err = command.Start(); err != nil {
		input.Close()
		output.Close()
		return "", err
	}
	failed := true
	defer func() {
		if failed {
			if runtime.GOOS == "windows" {
				_ = command.Process.Kill()
			} else {
				_ = command.Process.Signal(syscall.SIGTERM)
			}
			go func() {
				done := make(chan struct{})
				go func() { _ = command.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(80 * time.Second):
					_ = command.Process.Kill()
				}
			}()
			return
		}
		go command.Wait()
	}()
	if err = json.NewEncoder(input).Encode(launch); err != nil {
		input.Close()
		return "", err
	}
	input.Close()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
		output.Close()
	}()
	timer := time.NewTimer(workerStartupTimeout(launch.Transport))
	defer timer.Stop()
	select {
	case id := <-ready:
		if _, err = workerRecord(home, id); err != nil {
			if id == "" {
				// It exited without announcing itself; wait (briefly) for its last words.
				done := make(chan struct{})
				go func() { _ = command.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
				// shortcut: the log is shared, so another worker's line written meanwhile could be read instead.
				var stderr workerStderr
				_, _ = io.Copy(&stderr, io.NewSectionReader(log, logStart, 1<<20))
				if reason := stderr.lastLine(); reason != "" {
					return "", errors.New(reason)
				}
			}
			return "", fmt.Errorf("worker did not start; inspect %s: %w", log.Name(), err)
		}
		failed = false
		return id, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("worker startup timed out")
	}
}

func attachWorker(ctx context.Context, record WorkerRecord, local bool, since uint64) (*websocket.Conn, error) {
	headers := http.Header{"Authorization": []string{"Bearer " + record.Secret}}
	if local {
		headers.Set("X-Agentpack-Local-Terminal", "1")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	connection, response, err := dialer.DialContext(ctx, "ws://"+record.Address+"/attach?since="+strconv.FormatUint(since, 10), headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	return connection, err
}

// AttachTerminal opens a local-viewer connection to an existing worker without taking ownership of it.
func AttachTerminal(ctx context.Context, home, id string, since uint64) (*websocket.Conn, error) {
	record, err := workerRecord(home, id)
	if err != nil {
		return nil, err
	}
	connection, err := reattach(ctx, record, since)
	if err == nil {
		connection.SetReadLimit(replayLimit + 4096)
	}
	return connection, err
}

func StopWorker(ctx context.Context, home, id string) error {
	return controlWorker(ctx, home, id, "stop", "")
}

func RestartWorker(ctx context.Context, home, id, operationID string) error {
	if !validRestartID(operationID) {
		return errors.New("restart requires a valid operation_id")
	}
	return controlWorker(ctx, home, id, "restart", operationID)
}

var errWorkerControlRejected = errors.New("worker operation rejected")

func controlWorker(ctx context.Context, home, id, action, operationID string) error {
	record, err := workerRecord(home, id)
	if err != nil {
		return err
	}
	response, err := workerRequest(ctx, record, "POST", "/"+action, operationID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("%w: worker %s returned %s", errWorkerControlRejected, action, response.Status)
	}
	return nil
}

// workerStartupTimeout bounds how long a new worker may take to report ready.
// A chat worker also starts its agent, and the first chat on a machine
// installs the pinned npm adapter, which takes about half a minute; callers
// wait up to two minutes for a start.
func workerStartupTimeout(transport string) time.Duration {
	if transport == "acp" {
		return 100 * time.Second
	}
	return 30 * time.Second
}

// workerStderr keeps the end of a starting worker's stderr; its last line is the reason it stopped.
type workerStderr struct {
	mu   sync.Mutex
	tail []byte
}

func (w *workerStderr) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tail = append(w.tail, data...)
	if len(w.tail) > 4096 {
		w.tail = w.tail[len(w.tail)-4096:]
	}
	return len(data), nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// lastLine is the final meaningful line, without colour codes or the CLI's "ERROR" heading.
func (w *workerStderr) lastLine() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := strings.Split(ansiEscape.ReplaceAllString(string(w.tail), ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" && line != "ERROR" {
			return line
		}
	}
	return ""
}
