package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type Trace struct{ ID, Span string }

func NewTrace() Trace {
	var id [16]byte
	var span [8]byte
	rand.Read(id[:])
	rand.Read(span[:])
	return Trace{hex.EncodeToString(id[:]), hex.EncodeToString(span[:])}
}

// ParseTraceParent starts a fresh trace for malformed or all-zero identifiers.
func ParseTraceParent(value string) Trace {
	if len(value) == 55 && value[:3] == "00-" && value[35] == '-' && value[52] == '-' && (value[53:] == "00" || value[53:] == "01") {
		trace := Trace{value[3:35], value[36:52]}
		if validTrace(trace) {
			return trace
		}
	}
	return NewTrace()
}

func validTrace(trace Trace) bool {
	if len(trace.ID) != 32 || len(trace.Span) != 16 || trace.ID != strings.ToLower(trace.ID) || trace.Span != strings.ToLower(trace.Span) {
		return false
	}
	_, idErr := hex.DecodeString(trace.ID)
	_, spanErr := hex.DecodeString(trace.Span)
	return idErr == nil && spanErr == nil && trace.ID != strings.Repeat("0", 32) && trace.Span != strings.Repeat("0", 16)
}

func (t Trace) TraceParent() string { return "00-" + t.ID + "-" + t.Span + "-01" }

type traceKey struct{}

func WithTrace(ctx context.Context, header string) context.Context {
	return context.WithValue(ctx, traceKey{}, ParseTraceParent(header))
}
