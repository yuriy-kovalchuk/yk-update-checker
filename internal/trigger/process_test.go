package trigger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessRefusesSecondTriggerWhileRunning(t *testing.T) {
	// The child is this test binary running only TestHelperSleep.
	p := NewProcess(context.Background(), "-test.run=^TestHelperSleep$")
	if err := p.Trigger(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.Running(context.Background()) {
		t.Error("Running = false right after Trigger")
	}
	if err := p.Trigger(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Trigger = %v, want ErrAlreadyRunning", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.Running(context.Background()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.Running(context.Background()) {
		t.Error("still running after the child exited")
	}
}

func TestHelperSleep(*testing.T) { time.Sleep(200 * time.Millisecond) }
