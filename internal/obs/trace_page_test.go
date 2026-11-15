package obs

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTracePagesStayOrderedAcrossSpoolIngestionAndDayFiles(t *testing.T) {
	home, _, logs, clock := issueFixture(t)
	at := time.UnixMilli(clock.Load()).Add(-3 * time.Minute)
	trace := NewTrace().ID
	spool, err := OpenSpool(home, SpoolOptions{Proc: "worker", Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	var want []Event
	for i := range 70 {
		event := logEvent(at, "http")
		event.TraceID = trace
		event.SpanID = fmt.Sprintf("%016x", i+1)
		event.NodeID = []string{"n", "n0"}[i%2]
		if i%3 == 0 {
			event.TS = at.Add(-24 * time.Hour)
		}
		event.TS = event.TS.Add(time.Duration(i%5) * time.Nanosecond)
		want = append(want, event)
		if i%2 == 0 {
			logs.Write(event)
		} else {
			spool.Write(event)
		}
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	flushStore(t, logs)
	slices.SortFunc(want, CompareEvents)
	var got []Event
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		page, err := ReadTracePage(t.Context(), home, strings.ToUpper(trace), cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) > TracePageSize {
			t.Fatal("unbounded page")
		}
		got = append(got, page.Events...)
		if page.NextCursor == "" {
			break
		}
		if pageNumber == 0 {
			if _, err := logs.IngestSpool(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("paged trace lost or duplicated spans: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if CompareEvents(got[i], want[i]) != 0 {
			t.Fatalf("order at %d: %+v %+v", i, got[i], want[i])
		}
	}
	for _, bad := range []struct{ trace, cursor string }{{"", ""}, {"bad", ""}, {trace, "not-base64"}, {NewTrace().ID, cursor}} {
		if _, err := ReadTracePage(t.Context(), home, bad.trace, bad.cursor); err == nil {
			t.Fatalf("invalid trace page accepted: %+v", bad)
		}
	}
}
