package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTraceParentValidationAndPropagation(t *testing.T) {
	valid := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	trace := ParseTraceParent(valid)
	if trace.TraceParent() != valid {
		t.Fatal("valid trace changed")
	}
	for _, invalid := range []string{"", "garbage", valid + "x", strings.ToUpper(valid), strings.Replace(valid, "00-", "01-", 1), strings.Replace(valid, "0123456789abcdef0123456789abcdef", strings.Repeat("0", 32), 1), strings.Replace(valid, "-0123456789abcdef-", "-0000000000000000-", 1)} {
		got := ParseTraceParent(invalid)
		if got.ID == trace.ID || len(got.TraceParent()) != 55 {
			t.Fatalf("accepted malformed trace %q", invalid)
		}
	}
	ctx, parent := Start(WithTrace(context.Background(), valid), "http", "GET /api/sessions")
	childCtx, child := Start(ctx, "worker", "start")
	if From(childCtx) != child || parent.TraceID != trace.ID || child.TraceID != parent.TraceID || child.SpanID == parent.SpanID {
		t.Fatal("trace context lost")
	}
}

func TestEventEndsOnceWithBoundedRedactedAttributes(t *testing.T) {
	queue := NewQueue()
	ctx := WithSink(context.Background(), queue, Metadata{Version: "v0.13.9", Commit: "abc", NodeID: "node-a", Proc: "gateway"})
	_, event := Start(ctx, "http", "GET /api/sessions/{id}")
	event.Set("headers", http.Header{"Cookie": {"cookie-plaintext"}, "Authorization": {"Bearer bearer-plaintext"}, "X-Private": {"private-header"}, "Content-Type": {"application/json"}})
	event.Set("password", "plain-password")
	event.Set("token_value", "opaque-secret")
	event.Set("env", map[string]any{"UNEXPECTED": "plain-environment"})
	event.Set("prompt", "private-prompt")
	event.Set("nested", map[string]any{"value": "https://host.example/path?api_key=unknown-secret", "headers": map[string]any{"cookie": "nested-cookie", "content-type": "application/json"}})
	event.Fail(fmt.Errorf("cannot open /tmp/private-123: %w", errors.New("permission denied")))
	event.End()
	event.End()
	event.Set("late", "ignored")
	if len(queue.Events) != 1 {
		t.Fatal("End emitted more than once")
	}
	got := <-queue.Events
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"cookie-plaintext", "bearer-plaintext", "private-header", "plain-password", "opaque-secret", "plain-environment", "private-prompt", "unknown-secret", "nested-cookie", "late"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("event leaked %s", private)
		}
	}
	if got.Level != "error" || got.Fingerprint == "" || got.Stack == "" || got.TS.IsZero() || got.NodeID != "node-a" || got.Attrs["version"] != "v0.13.9" {
		t.Fatalf("event missing metadata: %+v", got)
	}
	if !strings.Contains(string(encoded), "application/json") {
		t.Fatal("allowlisted header missing")
	}
	if len(encoded) > MaxEventBytes {
		t.Fatalf("event has %d bytes", len(encoded))
	}
}

func TestFingerprintNormalizesIncidentalValues(t *testing.T) {
	makeEvent := func(message string) Event {
		return Event{Kind: "http", Stack: "github.com/olegpustovit/agentpack/internal/network.(*Gateway).Handler\n\t/tmp/source.go:19\n", Attrs: map[string]any{"errors": []any{map[string]any{"type": "*errors.errorString", "message": message}}}}
	}
	a := makeEvent(`open /tmp/one/session-abc123: request node-xyz123 failed after 12 retries for "Alice"`)
	b := makeEvent(`open /home/other/session-def456: request node-jkl456 failed after 45 retries for "Bob"`)
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("incidental values split one issue")
	}
	if Fingerprint(a) == Fingerprint(makeEvent("database is locked")) {
		t.Fatal("different failures grouped together")
	}
	b.Kind = "worker"
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("different kinds grouped together")
	}
	ui := Event{Kind: "ui.error", Attrs: map[string]any{"error_name": "TypeError", "message": "missing item 123", "route": "/sessions"}}
	next := ui
	next.Attrs = map[string]any{"error_name": "TypeError", "message": "missing item 456", "route": "/sessions"}
	if Fingerprint(ui) != Fingerprint(next) {
		t.Fatal("UI IDs split an issue")
	}
	next.Attrs["route"] = "/settings"
	if Fingerprint(ui) == Fingerprint(next) {
		t.Fatal("UI route ignored")
	}
}

func TestQueueNeverWaitsForStalledConsumer(t *testing.T) {
	queue := NewQueue()
	ctx := WithSink(context.Background(), queue, Metadata{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10000 {
			_, event := Start(ctx, "worker", "stop")
			event.End()
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stalled consumer blocked End")
	}
	if len(queue.Events) != 4096 || queue.Dropped.Load() != 5904 {
		t.Fatalf("queue len=%d drops=%d", len(queue.Events), queue.Dropped.Load())
	}
}

func TestRedactionDepthSizeAndConcurrentSets(t *testing.T) {
	nested := map[string]any{"leaf": "too-deep"}
	for range 10 {
		nested = map[string]any{"nested": nested}
	}
	queue := NewQueue()
	_, event := Start(WithSink(context.Background(), queue, Metadata{}), "test", "bounded")
	event.Set("deep", nested)
	event.Set("large", strings.Repeat("\x00", 100000))
	var writers sync.WaitGroup
	for i := range 10 {
		writers.Go(func() {
			for range 20 {
				event.Set(fmt.Sprint(i), "value")
			}
		})
	}
	writers.Wait()
	event.End()
	got := <-queue.Events
	data, err := json.Marshal(got)
	if err != nil || len(data) > MaxEventBytes {
		t.Fatalf("unbounded event: %d %v", len(data), err)
	}
	if strings.Contains(string(data), "too-deep") {
		t.Fatal("depth cap failed")
	}
}

func BenchmarkEvent(b *testing.B) {
	queue := NewQueue()
	ctx := WithSink(context.Background(), queue, Metadata{Version: "v0.13.9", Commit: "abc", NodeID: "node-a", Proc: "gateway"})
	for b.Loop() {
		_, event := Start(ctx, "http", "GET /api/sessions")
		event.Set("response_bytes", 120)
		event.End()
	}
}

func TestCredentialPolicy(t *testing.T) {
	for _, value := range []string{"Bearer\fopaque", "BASIC secret", "password=opaque", "token:opaque", "<local>", "https://user@host/path", "//user@host/path", "https://host/path?api.key=opaque", "https://host/path?key=opaque", "ghp_" + strings.Repeat("x", 20), "GITHUB_PAT_" + strings.Repeat("x", 20), "sk-" + strings.Repeat("x", 20), "xoxb-" + strings.Repeat("x", 20)} {
		if !CredentialShaped(value) {
			t.Fatalf("missed credential shape: %q", value)
		}
	}
	for _, key := range []string{"TOKEN", "api-key", "PrivateKey", "client_key", "password", "passwd", "auth", "signature", "paſſword"} {
		if !CredentialKey(key) {
			t.Fatalf("missed credential key %q", key)
		}
	}
	for _, value := range []string{"version", "v0.13.9", "node-abc", "response_bytes", "GET /api/sessions", "https://host/path"} {
		if CredentialShaped(value) {
			t.Fatalf("redacted ordinary value %q", value)
		}
	}
}
