package acp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDrainSessionUpdatesWaitsForHandlersAndHonorsCancellation(t *testing.T) {
	gate := newNotificationGate(strings.NewReader(""))
	gate.inflight = 2
	client := &HarnessClient{updates: gate}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.DrainSessionUpdates(ctx) }()
	gate.release()
	select {
	case err := <-done:
		t.Fatalf("drain ended with a handler still active: %v", err)
	default:
	}
	gate.release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	gate.inflight = 1
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := client.DrainSessionUpdates(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain ignored cancellation: %v", err)
	}
}
