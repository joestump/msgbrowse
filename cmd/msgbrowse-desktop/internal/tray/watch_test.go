package tray

import "testing"

// TestAwaitReady drives the three watchdog verdicts (issue #167) with manual
// channels — no real timers, fully deterministic.
func TestAwaitReady(t *testing.T) {
	closed := func() chan struct{} { c := make(chan struct{}); close(c); return c }

	t.Run("ready wins", func(t *testing.T) {
		if got := AwaitReady(closed(), make(chan struct{}), make(chan struct{})); got != Ready {
			t.Errorf("AwaitReady = %v, want Ready", got)
		}
	})

	t.Run("deadline reports timeout", func(t *testing.T) {
		if got := AwaitReady(make(chan struct{}), closed(), make(chan struct{})); got != Timeout {
			t.Errorf("AwaitReady = %v, want Timeout", got)
		}
	})

	t.Run("ready beats a simultaneous deadline", func(t *testing.T) {
		// Both fire: registration that DID complete must never be reported
		// as a timeout, whatever order select picks.
		for i := 0; i < 100; i++ {
			if got := AwaitReady(closed(), closed(), make(chan struct{})); got != Ready {
				t.Fatalf("AwaitReady = %v on iteration %d, want Ready", got, i)
			}
		}
	})

	t.Run("shutdown cancels quietly", func(t *testing.T) {
		if got := AwaitReady(make(chan struct{}), make(chan struct{}), closed()); got != Cancelled {
			t.Errorf("AwaitReady = %v, want Cancelled", got)
		}
	})
}
