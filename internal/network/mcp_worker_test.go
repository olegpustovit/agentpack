//go:build linux || darwin

package network

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPWorkerOutputAndSharedInput(t *testing.T) {
	session := testSession(t)
	local, replay := attachTest(t, session, true)
	readUntil(t, local, string(replay.Data), "READY")
	secret := strings.Repeat("s", 64)
	handler, err := WorkerHandler(session, secret)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	status, data := request("GET", "/output?limit=8", "")
	var output TerminalOutput
	if status != 200 || json.Unmarshal(data, &output) != nil || len(output.Output) > 8 || output.NextOffset != uint64(len(output.Output)) {
		t.Fatalf("output: %d %s", status, data)
	}
	for _, path := range []string{"/output?limit=32769", "/output?offset=-1", "/output?offset=18446744073709551615"} {
		if status, _ := request("GET", path, ""); status != 400 {
			t.Fatal("accepted bad output range", path)
		}
	}
	for range maxAttachments - 1 {
		if _, _, err := session.Attach(false, 0); err != nil {
			t.Fatal("could not fill observer limit for MCP input test:", err)
		}
	}
	responder := session.State().Responder
	if responder != local.ID {
		t.Fatalf("local should remain the query responder: %d != %d", responder, local.ID)
	}
	if status, data := request("POST", "/input", "from MCP\n"); status != 200 {
		t.Fatalf("write: %d %s", status, data)
	}
	if got := session.State().Responder; got != responder {
		t.Fatalf("MCP input changed terminal-query responder: %d != %d", got, responder)
	}
	readUntil(t, local, "", "ECHO from MCP")
	if !session.State().SharedInput {
		t.Fatal("new session did not advertise shared input")
	}
	if status, _ := request("POST", "/input", strings.Repeat("x", chunkSize+1)); status != 400 {
		t.Fatal("oversized input accepted")
	}
}
