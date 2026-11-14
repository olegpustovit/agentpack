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
		id, span := value[3:35], value[36:52]
		_, idErr := hex.DecodeString(id)
		_, spanErr := hex.DecodeString(span)
		if idErr == nil && spanErr == nil && id != strings.Repeat("0", 32) && span != strings.Repeat("0", 16) && value == strings.ToLower(value) {
			return Trace{id, span}
		}
	}
	return NewTrace()
}

func (t Trace) TraceParent() string { return "00-" + t.ID + "-" + t.Span + "-01" }

type traceKey struct{}

func WithTrace(ctx context.Context, header string) context.Context {
	return context.WithValue(ctx, traceKey{}, ParseTraceParent(header))
}
