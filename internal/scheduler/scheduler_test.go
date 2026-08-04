package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartFiresImmediatelyAndOnInterval(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(20*time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	})

	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("scheduler made %d calls, want at least 3 (initial + ticks)", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after context cancel")
	}
}

func TestStartStopsFiringOnCancel(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())

	s := New(10*time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	})

	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()

	// Let at least one tick land before cancelling.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("scheduler made %d calls, want at least 2 before cancel", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after context cancel")
	}

	// Start has returned, so no further calls are possible.
	final := calls.Load()
	time.Sleep(50 * time.Millisecond) // ~5 intervals
	if got := calls.Load(); got != final {
		t.Errorf("scheduler fired %d time(s) after cancel (%d → %d)", got-final, final, got)
	}
}
