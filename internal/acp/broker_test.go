package acp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBrokerFirstAnswerWins(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin("p1"); err != nil {
		t.Fatal(err)
	}
	if !b.Answer("p1", "allow") {
		t.Fatal("first answer should succeed")
	}
	if b.Answer("p1", "deny") {
		t.Fatal("second answer should lose")
	}
	got, err := b.Wait(context.Background(), "p1")
	if err != nil || got != "allow" {
		t.Fatalf("Wait() = %q, %v; want allow", got, err)
	}
	b.End("p1")
}

func TestBrokerFirstAnswerWakesAllWaiters(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin("shared"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		option string
		err    error
	}
	started := make(chan struct{}, 2)
	results := make(chan result, 2)
	for range 2 {
		go func() {
			started <- struct{}{}
			option, err := b.Wait(context.Background(), "shared")
			results <- result{option: option, err: err}
		}()
	}
	for range 2 {
		<-started
	}
	if !b.Answer("shared", "allow") {
		t.Fatal("answer failed")
	}
	for range 2 {
		got := <-results
		if got.err != nil || got.option != "allow" {
			t.Fatalf("Wait() = %q, %v; want allow", got.option, got.err)
		}
	}
	b.End("shared")
}

func TestBrokerWaitCancellationWithoutAnswer(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin("pending"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Wait(ctx, "pending"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v; want context canceled", err)
	}
	b.End("pending")
}

func TestBrokerRequestIDReuseDoesNotReuseAnswer(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin("reuse"); err != nil {
		t.Fatal(err)
	}
	if !b.Answer("reuse", "allow") {
		t.Fatal("first answer failed")
	}
	if got, err := b.Wait(context.Background(), "reuse"); err != nil || got != "allow" {
		t.Fatalf("first Wait() = %q, %v; want allow", got, err)
	}
	b.End("reuse")

	if err := b.Begin("reuse"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := b.Wait(ctx, "reuse"); !errors.Is(err, context.Canceled) || got != "" {
		t.Fatalf("reused ID Wait() = %q, %v; want cancellation without inherited answer", got, err)
	}
	b.End("reuse")
	if b.Answer("reuse", "deny") {
		t.Fatal("an answer after the request finished should be rejected")
	}
}

func TestBrokerRejectsEmptyUnknownAndDuplicateIDs(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin(""); err == nil {
		t.Fatal("empty request ID should be rejected")
	}
	if b.Answer("", "allow") || b.Answer("unknown", "allow") {
		t.Fatal("empty or unknown request ID should not be answered")
	}
	if _, err := b.Wait(context.Background(), ""); err == nil {
		t.Fatal("empty request ID should not be waited on")
	}
	if _, err := b.Wait(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown request ID should not be waited on")
	}
	if err := b.Begin("active"); err != nil {
		t.Fatal(err)
	}
	if err := b.Begin("active"); err == nil {
		t.Fatal("duplicate active request ID should be rejected")
	}
	b.End("active")
}

func TestBrokerPermissionAnswerRequiresActiveRequestAndSuccessfulForward(t *testing.T) {
	var calls atomic.Int32
	b := NewBroker(nil, nil, func(context.Context, string, string) error {
		if calls.Add(1) == 1 {
			return errors.New("forward failed")
		}
		return nil
	}, nil)
	if err := b.AnswerPermission(context.Background(), "unknown", "allow"); err == nil {
		t.Fatal("unknown permission should be rejected")
	}
	if calls.Load() != 0 {
		t.Fatal("unknown permission was forwarded")
	}
	if err := b.Begin("permission"); err != nil {
		t.Fatal(err)
	}
	if err := b.AnswerPermission(context.Background(), "permission", "allow"); err == nil {
		t.Fatal("forwarding failure should be reported")
	}
	if err := b.AnswerPermission(context.Background(), "permission", "allow"); err != nil {
		t.Fatal(err)
	}
	if err := b.AnswerPermission(context.Background(), "permission", "deny"); err == nil {
		t.Fatal("duplicate answer should be rejected")
	}
	if got, err := b.Wait(context.Background(), "permission"); err != nil || got != "allow" {
		t.Fatalf("Wait() = %q, %v; want allow", got, err)
	}
	b.End("permission")
}

func TestBrokerAnswerRace(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.Begin("race"); err != nil {
		t.Fatal(err)
	}
	const n = 32
	var wins atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(id int) {
			defer wg.Done()
			opt := "allow"
			if id%2 == 1 {
				opt = "deny"
			}
			if b.Answer("race", opt) {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("expected exactly one winning Answer, got %d", wins.Load())
	}
	got, err := b.Wait(context.Background(), "race")
	if err != nil {
		t.Fatal(err)
	}
	if got != "allow" && got != "deny" {
		t.Fatalf("unexpected option %q", got)
	}
	b.End("race")
}

func TestBrokerSetConfigUnavailable(t *testing.T) {
	b := NewBroker(nil, nil, nil, nil)
	if err := b.SetConfig(context.Background(), "mode", "default"); err == nil {
		t.Fatal("expected error")
	}
}
