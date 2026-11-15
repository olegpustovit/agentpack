// Package obs records bounded, redacted events without opening runtime state on
// the launch path. Storage and process spooling implement Sink separately.
package obs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

var currentSchema = state.SchemaVersion()

type Event struct {
	TS            time.Time      `json:"ts"`
	DurationUS    int64          `json:"duration_us"`
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	NodeID        string         `json:"node_id"`
	Proc          string         `json:"proc"`
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Level         string         `json:"level"`
	ErrorTrace    bool           `json:"error_trace,omitempty"`
	Status        int            `json:"status"`
	SessionID     string         `json:"session_id,omitempty"`
	User          string         `json:"user,omitempty"`
	Fingerprint   string         `json:"fingerprint,omitempty"`
	Stack         string         `json:"stack,omitempty"`
	Attrs         map[string]any `json:"attrs"`
	life          *lifecycle
	source        string
	sourceLine    int64
	errorPosition int64
}

type lifecycle struct {
	sync.Mutex
	started time.Time
	ended   bool
	sink    Sink
}

// Write must return immediately. A slow consumer drops at its bounded queue.
type Sink interface{ Write(Event) }

type Queue struct {
	Events  chan Event
	Dropped atomic.Uint64
}

func NewQueue() *Queue { return &Queue{Events: make(chan Event, 4096)} }
func (q *Queue) Write(event Event) {
	select {
	case q.Events <- event:
	default:
		q.Dropped.Add(1)
	}
}

type Metadata struct{ Version, Commit, NodeID, Proc string }

func BuildMetadata(version, node, proc string) Metadata {
	metadata := Metadata{Version: version, NodeID: node, Proc: proc}
	if build, ok := debug.ReadBuildInfo(); ok {
		if metadata.Version == "" {
			metadata.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				metadata.Commit = setting.Value
				break
			}
		}
	}
	return metadata
}

type recording struct {
	sink     Sink
	metadata Metadata
}
type recordingKey struct{}
type eventKey struct{}

func WithSink(ctx context.Context, sink Sink, metadata Metadata) context.Context {
	return context.WithValue(ctx, recordingKey{}, recording{sink, metadata})
}

// WithRecording keeps request cancellation and tracing while inheriting a process sink.
func WithRecording(ctx, source context.Context) context.Context {
	if rec, ok := source.Value(recordingKey{}).(recording); ok {
		return context.WithValue(ctx, recordingKey{}, rec)
	}
	return ctx
}

func Start(ctx context.Context, kind, name string) (context.Context, *Event) {
	trace := NewTrace()
	if parent := From(ctx); parent != nil {
		trace.ID = parent.TraceID
	} else if inherited, ok := ctx.Value(traceKey{}).(Trace); ok {
		trace.ID = inherited.ID
	}
	rec, _ := ctx.Value(recordingKey{}).(recording)
	e := &Event{TraceID: trace.ID, SpanID: trace.Span, NodeID: rec.metadata.NodeID, Proc: rec.metadata.Proc, Kind: kind, Name: name, Level: "info", Attrs: map[string]any{
		"version": rec.metadata.Version, "commit": rec.metadata.Commit, "os": runtime.GOOS, "arch": runtime.GOARCH, "schema_version": currentSchema, "pid": os.Getpid(),
	}, life: &lifecycle{started: time.Now(), sink: rec.sink}}
	return context.WithValue(ctx, eventKey{}, e), e
}

func From(ctx context.Context) *Event { event, _ := ctx.Value(eventKey{}).(*Event); return event }
func (e *Event) TraceParent() string  { return (Trace{e.TraceID, e.SpanID}).TraceParent() }

func (e *Event) Set(key string, value any) {
	if e == nil || e.life == nil {
		return
	}
	e.life.Lock()
	defer e.life.Unlock()
	if !e.life.ended {
		switch key {
		case "user":
			if user, ok := value.(string); ok {
				e.User = user
				return
			}
		case "session_id":
			if session, ok := value.(string); ok {
				e.SessionID = session
				return
			}
		}
		e.Attrs[key] = value
	}
}

func (e *Event) Fail(err error) {
	if e == nil || e.life == nil || err == nil {
		return
	}
	e.life.Lock()
	defer e.life.Unlock()
	if e.life.ended {
		return
	}
	e.Level = "error"
	chain := []any{}
	var collect func(error, int)
	collect = func(err error, depth int) {
		if err == nil || depth >= 16 || len(chain) >= 16 {
			return
		}
		chain = append(chain, map[string]any{"type": fmt.Sprintf("%T", err), "message": err.Error()})
		switch err := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range err.Unwrap() {
				collect(child, depth+1)
			}
		case interface{ Unwrap() error }:
			collect(err.Unwrap(), depth+1)
		}
	}
	collect(err, 0)
	e.Attrs["errors"] = chain
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	e.Attrs["snapshot"] = map[string]any{
		"goroutines": runtime.NumGoroutine(), "heap_bytes": memory.Alloc, "memory_bytes": memory.Sys,
	}
	var pcs [32]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	var stack strings.Builder
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&stack, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	e.Stack = stack.String()
}

func (e *Event) End() {
	if e == nil || e.life == nil {
		return
	}
	e.life.Lock()
	defer e.life.Unlock()
	if e.life.ended {
		return
	}
	e.life.ended = true
	e.TS = time.Now().UTC()
	e.DurationUS = time.Since(e.life.started).Microseconds()
	if e.Level == "error" {
		e.Fingerprint = Fingerprint(*e)
	}
	event := e.Sanitized()
	if e.life.sink != nil {
		e.life.sink.Write(event)
	}
}

// Sanitized is also used at spool and browser ingestion boundaries.
func (e Event) Sanitized() Event { return e.limited(MaxEventBytes) }

func (e Event) limited(maxBytes int) Event {
	e.life = nil
	trace := ParseTraceParent((Trace{e.TraceID, e.SpanID}).TraceParent())
	e.TraceID, e.SpanID = trace.ID, trace.Span
	if decoded, err := hex.DecodeString(e.Fingerprint); e.Level == "error" && (err != nil || len(decoded) != 32) {
		e.Fingerprint = Fingerprint(e)
	}
	if e.Level != "error" {
		e.Level = "info"
		e.Fingerprint = ""
	}
	e.Kind, e.Name, e.NodeID, e.Proc = boundedString(redactString(e.Kind), 256), boundedString(redactString(e.Name), 256), boundedString(redactString(e.NodeID), 256), boundedString(redactString(e.Proc), 256)
	e.SessionID, e.User = boundedString(redactString(e.SessionID), 256), boundedString(redactString(e.User), 256)
	e.Stack = boundedString(redactString(e.Stack), 8192)
	original := e.Attrs
	e.Attrs, _ = Redact(original).(map[string]any)
	if e.Attrs == nil {
		e.Attrs = map[string]any{}
	}
	for _, key := range metadataKeys {
		if _, ok := e.Attrs[key]; !ok {
			e.Attrs[key] = metadataValue(key, original[key])
		}
	}
	if !eventFits(e, maxBytes) {
		e.Attrs = essentialAttrs(original)
		e.Stack = boundedString(e.Stack, 2048)
		if !eventFits(e, maxBytes) {
			e.Stack = ""
			e.Kind, e.Name, e.NodeID, e.Proc = boundedString(e.Kind, 128), boundedString(e.Name, 128), boundedString(e.NodeID, 128), boundedString(e.Proc, 128)
			e.SessionID, e.User = boundedString(e.SessionID, 128), boundedString(e.User, 128)
			for _, key := range metadataKeys {
				if value, ok := e.Attrs[key].(string); ok {
					e.Attrs[key] = boundedString(value, 128)
				}
			}
		}
	}
	return e
}

var metadataKeys = []string{"version", "commit", "os", "arch", "schema_version", "pid"}

func metadataValue(key string, value any) any {
	if key != "schema_version" && key != "pid" {
		text, _ := value.(string)
		return boundedString(redactString(text), 256)
	}
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return number
	case uint64:
		return number
	case float64:
		if !math.IsInf(number, 0) && !math.IsNaN(number) {
			return number
		}
	}
	return 0
}

func essentialAttrs(attrs map[string]any) map[string]any {
	out := map[string]any{"truncated": true}
	for _, key := range metadataKeys {
		out[key] = metadataValue(key, attrs[key])
	}
	if chain, ok := attrs["errors"].([]any); ok && len(chain) > 0 {
		if first, ok := chain[0].(map[string]any); ok {
			message, _ := first["message"].(string)
			kind, _ := first["type"].(string)
			out["errors"] = []any{map[string]any{"type": boundedString(redactString(kind), 128), "message": boundedString(redactString(message), 512)}}
		}
	}
	return out
}

func eventFits(event Event, maxBytes int) bool {
	data, err := json.Marshal(event)
	return err == nil && len(data) <= maxBytes
}
