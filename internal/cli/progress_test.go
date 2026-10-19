package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestProgressReportsSlowWorkAndStops(t *testing.T) {
	var output bytes.Buffer
	writer := &progressWriter{Writer: &output}
	stop := startProgress(writer, "lock", time.Millisecond)
	deadline := time.After(time.Second)
	for {
		writer.mu.Lock()
		shown := strings.Contains(output.String(), "agentpack: lock…")
		writer.mu.Unlock()
		if shown {
			break
		}
		select {
		case <-deadline:
			stop()
			t.Fatal("slow operation had no progress")
		case <-time.After(time.Millisecond):
		}
	}
	stop()
	stop()
	before := output.String()
	time.Sleep(10 * time.Millisecond)
	if output.String() != before {
		t.Fatal("progress continued after completion")
	}
}

func TestFastOperationHasNoProgress(t *testing.T) {
	var output bytes.Buffer
	stop := startProgress(&output, "lock", time.Hour)
	stop()
	if output.Len() != 0 {
		t.Fatalf("fast operation printed %q", output.String())
	}
}
