package obs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestForwardErrorsRetriesLateFailuresAndMainChanges(t *testing.T) {
	ctx := t.Context()
	home, mainHome := t.TempDir(), t.TempDir()
	source, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	main, err := Open(mainHome, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	before := logEvent(time.Now(), "healthy-before-failure")
	healthy := logEvent(time.Now(), "healthy")
	failure := logEvent(time.Now(), "failed")
	failure.Level = "error"
	for _, event := range []Event{before, healthy, failure} {
		source.Write(event)
	}
	calls := 0
	send := func(ctx context.Context, batch []Event) error {
		calls++
		for _, event := range batch {
			if !event.ErrorTrace || event.TraceID == healthy.TraceID {
				t.Fatalf("forwarded a healthy trace: %+v", event)
			}
		}
		return main.Import(ctx, batch)
	}
	if err := source.ForwardErrors(ctx, "", send); err != nil || calls != 0 {
		t.Fatalf("forwarded without main: %d %v", calls, err)
	}
	if err := source.ForwardErrors(ctx, "main", func(ctx context.Context, batch []Event) error {
		if err := send(ctx, batch); err != nil {
			return err
		}
		return errors.New("connection lost after commit")
	}); err == nil {
		t.Fatal("acknowledged an unsuccessful upload")
	}
	if err := source.ForwardErrors(ctx, "main", send); err != nil || calls != 2 {
		t.Fatalf("retry: %d %v", calls, err)
	}
	if err := source.ForwardErrors(ctx, "main", send); err != nil || calls != 2 {
		t.Fatalf("cursor replayed acknowledged rows: %d %v", calls, err)
	}
	late := before
	late.Level, late.SpanID = "error", NewTrace().Span
	source.Write(late)
	if err := source.ForwardErrors(ctx, "main", send); err != nil {
		t.Fatal(err)
	}
	for _, trace := range []struct {
		id    string
		count int
	}{{before.TraceID, 2}, {failure.TraceID, 1}, {healthy.TraceID, 0}} {
		events, err := TraceEvents(ctx, mainHome, trace.id)
		if err != nil || len(events) != trace.count {
			t.Fatalf("retained trace after retry: %+v %v", events, err)
		}
	}
	child := before
	child.SpanID = NewTrace().Span
	source.Write(child)
	if err := source.ForwardErrors(ctx, "main", send); err != nil {
		t.Fatal(err)
	}
	if err := source.ForwardErrors(ctx, "new-main", send); err != nil {
		t.Fatal(err)
	}
	events, err := TraceEvents(ctx, mainHome, before.TraceID)
	if err != nil || len(events) != 3 {
		t.Fatalf("late span or main handover lost events: %+v %v", events, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	previous := calls
	if err := restarted.ForwardErrors(ctx, "main", send); err != nil || calls != previous {
		t.Fatalf("restart forgot cursor: %d -> %d %v", previous, calls, err)
	}
}

func TestErrorTraceSchemaUpgradesExistingDay(t *testing.T) {
	home := t.TempDir()
	source, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	event := logEvent(time.Now(), "old-error")
	event.Level = "error"
	source.Write(event)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	days, err := logDays(home)
	if err != nil || len(days) != 1 {
		t.Fatalf("day fixture: %v %v", days, err)
	}
	write, err := openLogFile(filepath.Join(home, "logs", days[0]), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.Exec("DROP INDEX events_error_trace; ALTER TABLE events DROP COLUMN error_trace"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	events, err := TraceEvents(t.Context(), home, event.TraceID)
	if err != nil || len(events) != 1 || !events[0].ErrorTrace {
		t.Fatalf("legacy read: %+v %v", events, err)
	}
	restarted, err := Open(home, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	child := event
	child.Level, child.SpanID = "info", NewTrace().Span
	restarted.Write(child)
	flushStore(t, restarted)
	events, err = TraceEvents(t.Context(), home, event.TraceID)
	if err != nil || len(events) != 2 || !events[0].ErrorTrace || !events[1].ErrorTrace {
		t.Fatalf("schema upgrade lost failed trace: %+v %v", events, err)
	}
}

func TestForwardErrorsDrainsBoundedBatches(t *testing.T) {
	source, err := Open(t.TempDir(), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for range 40 {
		event := logEvent(time.Now(), "failed")
		event.Level = "error"
		source.Write(event)
	}
	calls, count := 0, 0
	seen := map[string]bool{}
	if err := source.ForwardErrors(t.Context(), "main", func(_ context.Context, batch []Event) error {
		calls++
		if len(batch) > 16 {
			t.Fatalf("oversized batch: %d", len(batch))
		}
		for _, event := range batch {
			if seen[event.SpanID] {
				t.Fatal("duplicate event within forwarding pass")
			}
			seen[event.SpanID] = true
		}
		count += len(batch)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || count != 40 {
		t.Fatalf("batching lost events: %d calls, %d events", calls, count)
	}
}
