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

func TestSpinnerShowsTheStepKeepsLogLinesAndLeavesTheLineBlank(t *testing.T) {
	var output bytes.Buffer
	writer := &progressWriter{Writer: &output}
	writer.draw("⠋ preparing claude: a very long step name", 20)
	writer.draw("⠙ short", 20)
	if _, err := writer.Write([]byte("warning: kept\n")); err != nil {
		t.Fatal(err)
	}
	stop := runSpinner(writer, func() string { return "staging" }, 80)
	deadline := time.Now().Add(5 * time.Second)
	for {
		writer.mu.Lock()
		shown := strings.Contains(output.String(), "staging")
		writer.mu.Unlock()
		if shown || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	text := output.String()
	if !strings.Contains(text, "…") || !strings.Contains(text, "\rwarning: kept\n") {
		t.Fatalf("spinner output: %q", text)
	}
	if !strings.Contains(text, "staging") || !strings.HasSuffix(text, "\r") || writer.width != 0 {
		t.Fatalf("spinner did not clean up: %q", text)
	}
	if strings.ContainsRune(text, 0x1b) {
		t.Fatalf("spinner used escape sequences: %q", text)
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

func TestProgressNamesTheCurrentStep(t *testing.T) {
	var output bytes.Buffer
	writer := &progressWriter{Writer: &output}
	lines := make(chan struct{})
	stop := watchProgress(writer, func() string {
		defer func() {
			select {
			case lines <- struct{}{}:
			default:
			}
		}()
		return "preparing claude: staging"
	}, time.Millisecond)
	<-lines
	stop()
	if !strings.Contains(output.String(), "agentpack: preparing claude: staging… (") {
		t.Fatalf("progress did not name the step: %q", output.String())
	}
}
