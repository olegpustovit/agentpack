package network

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

type SessionInfo struct {
	LastUsed       time.Time     `json:"last_used,omitzero"`
	FolderID       string        `json:"folder_id,omitempty"`
	ID             string        `json:"id"`
	Name           string        `json:"name,omitempty"`
	Directory      string        `json:"directory"`
	DefinitionRoot string        `json:"definition_root,omitempty"`
	Harness        string        `json:"harness"`
	Mode           string        `json:"mode"`
	State          TerminalState `json:"state"`
	StartedAt      time.Time     `json:"started_at,omitzero"`
	StartedBy      *Actor        `json:"started_by,omitempty"`
}

func workerRecord(home, id string) (WorkerRecord, error) {
	var record WorkerRecord
	if !strings.HasPrefix(id, "session-") || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return record, errors.New("invalid session ID")
	}
	data, err := os.ReadFile(filepath.Join(home, "network", "sessions", id, "worker.json"))
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	host, port, err := net.SplitHostPort(record.Address)
	if err != nil || host != "127.0.0.1" || port == "" || record.ID != id || len(record.Secret) != 64 {
		return WorkerRecord{}, errors.New("invalid worker record")
	}
	return record, nil
}

func workerRequest(ctx context.Context, record WorkerRecord, method, path, operationID string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://"+record.Address+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+record.Secret)
	if operationID != "" {
		request.Header.Set("Idempotency-Key", operationID)
	}
	timeout := 2 * time.Second
	if method == http.MethodPost && (path == "/stop" || path == "/restart") {
		timeout = 2 * time.Minute
	}
	client := http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	return client.Do(request)
}

func Sessions(ctx context.Context, home string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(filepath.Join(home, "network", "sessions"))
	if os.IsNotExist(err) {
		return []SessionInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []SessionInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := workerRecord(home, entry.Name())
		if err != nil {
			continue
		}
		state, err := sessionState(ctx, record)
		if err != nil {
			continue
		}
		info := record.Session
		info.ID = record.ID
		info.State = state
		name, err := readSessionName(home, record.ID)
		if err != nil {
			return nil, fmt.Errorf("read session name: %w", err)
		}
		info.Name = name
		result = append(result, info)
	}
	return result, nil
}

func StartWorker(ctx context.Context, home, executable string, launch WorkerLaunch) (string, error) {
	if launch.Session.StartedAt.IsZero() {
		launch.Session.StartedAt = time.Now()
	}
	directory := filepath.Join(home, "network")
	if err := EnsurePrivateDirectory(directory); err != nil {
		return "", err
	}
	log, err := os.OpenFile(filepath.Join(directory, "workers.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	command := exec.Command(executable, "session-worker")
	ConfigureDetached(command)
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
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case id := <-ready:
		if _, err = workerRecord(home, id); err != nil {
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
