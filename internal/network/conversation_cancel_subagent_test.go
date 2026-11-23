package network

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olegpustovit/agentpack/internal/acp"
)

func TestConversationCancelRouteCancelsOneSubagent(t *testing.T) {
	log := acp.NewEventLog(0)
	parentCancels := 0
	broker := acp.NewBroker(nil, func(context.Context) error { parentCancels++; return nil }, nil, nil)
	toAgent, fromClient := io.Pipe()
	fromAgent, _ := io.Pipe()
	acp.NewHarnessClient("", log, broker).NewClientSideConnection(fromClient, fromAgent)
	acp.AppendRawSessionUpdate(log, []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"parent","update":{"sessionUpdate":"subagent_update","sessionId":"child-1","capabilities":{"cancel":{}}}}}`))
	mux := http.NewServeMux()
	(&WorkerHTTP{Conversation: NewConversationSession(log, broker)}).RegisterConversation(mux)
	post := func(body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/conversation/cancel", strings.NewReader(body)))
		return w.Code
	}

	for _, body := range []string{`{"subagent":"tool:toolu_1"}`, `{"subagent":"unknown"}`, `{`} {
		if code := post(body); code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, code)
		}
	}
	sent := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(toAgent).ReadString('\n')
		sent <- line
	}()
	if code := post(`{"subagent":"child-1"}`); code != http.StatusOK {
		t.Fatalf("child cancel status %d", code)
	}
	if line := <-sent; !strings.Contains(line, `"session/cancel"`) || !strings.Contains(line, `"sessionId":"child-1"`) || parentCancels != 0 {
		t.Fatalf("child cancel sent %s, parent cancels %d", line, parentCancels)
	}
	for _, body := range []string{"", `{}`} {
		if code := post(body); code != http.StatusOK {
			t.Fatalf("parent cancel %q: status %d", body, code)
		}
	}
	if parentCancels != 2 {
		t.Fatalf("parent cancels = %d", parentCancels)
	}
}
