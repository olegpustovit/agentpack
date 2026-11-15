package obs

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const TracePageSize = 16

type TracePage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type tracePosition struct {
	At    int64  `json:"at"`
	Node  string `json:"node"`
	Span  string `json:"span"`
	Trace string `json:"trace"`
}

// CompareEvents uses the persisted timestamp precision and stable identities across spool ingestion.
func CompareEvents(a, b Event) int {
	if order := cmp.Compare(a.TS.UnixMicro(), b.TS.UnixMicro()); order != 0 {
		return order
	}
	if order := strings.Compare(a.NodeID, b.NodeID); order != 0 {
		return order
	}
	if order := strings.Compare(a.TraceID, b.TraceID); order != 0 {
		return order
	}
	return strings.Compare(a.SpanID, b.SpanID)
}

func TraceCursor(event Event) string {
	body, _ := json.Marshal(tracePosition{At: event.TS.UnixMicro(), Node: event.NodeID, Span: event.SpanID, Trace: event.TraceID})
	return base64.RawURLEncoding.EncodeToString(body)
}

func ParseTraceCursor(trace, cursor string) (Event, error) {
	if len(cursor) > 8192 {
		return Event{}, errors.New("invalid trace cursor")
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	var after tracePosition
	if err != nil || json.Unmarshal(body, &after) != nil || after.Trace != strings.ToLower(trace) || len(after.Node) > 4096 {
		return Event{}, errors.New("invalid trace cursor")
	}
	span, err := hex.DecodeString(after.Span)
	if err != nil || len(span) != 8 {
		return Event{}, errors.New("invalid trace cursor")
	}
	return Event{TS: time.UnixMicro(after.At).UTC(), NodeID: after.Node, TraceID: after.Trace, SpanID: strings.ToLower(after.Span)}, nil
}

// ReadTracePage reads a bounded page without loading the complete retained trace.
func ReadTracePage(ctx context.Context, home, trace, cursor string) (TracePage, error) {
	if trace == "" {
		return TracePage{}, errors.New("trace ID is required")
	}
	filter := Filter{Trace: trace, Limit: TracePageSize + 1}
	if cursor != "" {
		after, err := ParseTraceCursor(trace, cursor)
		if err != nil {
			return TracePage{}, err
		}
		filter.traceAfter = &tracePosition{At: after.TS.UnixMicro(), Node: after.NodeID, Span: after.SpanID}
	}
	events, err := queryEvents(ctx, home, filter)
	if err != nil {
		return TracePage{}, err
	}
	page := TracePage{Events: events}
	if len(events) > TracePageSize {
		page.Events = events[:TracePageSize]
		page.NextCursor = TraceCursor(page.Events[len(page.Events)-1])
	}
	return page, nil
}
