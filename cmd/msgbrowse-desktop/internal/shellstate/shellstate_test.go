// Headless tests for the shell lifecycle state machine, run with
// CGO_ENABLED=0 by `make desktop-test`. The pre-startup latch tests pin the
// startup-race fix from #114's review: a quit request arriving before Wails
// OnStartup must be replayed, not dropped.
package shellstate

import (
	"context"
	"sync"
	"testing"
)

// TestPreStartupQuitIsLatchedAndReplayed is the #114 review scenario: a
// signal lands between embedded.Start and OnStartup. RequestQuit has no
// context to fire on (returns nil), but SetContext must replay the latched
// request exactly once.
func TestPreStartupQuitIsLatchedAndReplayed(t *testing.T) {
	var lc Lifecycle

	if fire := lc.RequestQuit(); fire != nil {
		t.Fatalf("RequestQuit before startup returned %v; want nil (latched)", fire)
	}

	ctx := context.Background()
	if fire := lc.SetContext(ctx); fire != ctx {
		t.Fatalf("SetContext after a latched quit returned %v; want the runtime context (replay)", fire)
	}

	// The replay already fired; nothing may fire twice.
	if fire := lc.RequestQuit(); fire != nil {
		t.Errorf("RequestQuit after the replay returned %v; want nil (already fired)", fire)
	}
}

// TestNormalStartupThenQuit covers the ordinary path: startup first (no
// replay), then a single quit fires on the runtime context and later
// requests are no-ops.
func TestNormalStartupThenQuit(t *testing.T) {
	var lc Lifecycle
	ctx := context.Background()

	if fire := lc.SetContext(ctx); fire != nil {
		t.Fatalf("SetContext with no pending quit returned %v; want nil", fire)
	}
	if lc.QuitRequested() {
		t.Error("QuitRequested = true before any request")
	}

	if fire := lc.RequestQuit(); fire != ctx {
		t.Fatalf("RequestQuit = %v; want the runtime context", fire)
	}
	if !lc.QuitRequested() {
		t.Error("QuitRequested = false after a request")
	}
	if fire := lc.RequestQuit(); fire != nil {
		t.Errorf("second RequestQuit = %v; want nil (fire once)", fire)
	}
}

// TestMarkShutdownSuppressesLaterQuits covers quit paths that bypass
// RequestQuit entirely (e.g. Cmd+Q on macOS): once OnShutdown has run,
// RequestQuit must never fire again.
func TestMarkShutdownSuppressesLaterQuits(t *testing.T) {
	var lc Lifecycle
	lc.SetContext(context.Background())
	lc.MarkShutdown()

	if fire := lc.RequestQuit(); fire != nil {
		t.Errorf("RequestQuit after MarkShutdown = %v; want nil", fire)
	}
	if !lc.QuitRequested() {
		t.Error("QuitRequested = false after MarkShutdown")
	}
}

// TestContextAccessor covers the tray-action guard: nil before startup, the
// runtime context after.
func TestContextAccessor(t *testing.T) {
	var lc Lifecycle
	if lc.Context() != nil {
		t.Error("Context before startup should be nil")
	}
	ctx := context.Background()
	lc.SetContext(ctx)
	if lc.Context() != ctx {
		t.Error("Context after startup should be the runtime context")
	}
}

// TestConcurrentQuitFiresOnce hammers RequestQuit from many goroutines
// racing SetContext; exactly one caller (or the replay) may receive a
// context to fire on.
func TestConcurrentQuitFiresOnce(t *testing.T) {
	var lc Lifecycle
	ctx := context.Background()

	var mu sync.Mutex
	fired := 0
	record := func(c context.Context) {
		if c != nil {
			mu.Lock()
			fired++
			mu.Unlock()
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(lc.RequestQuit())
		}()
	}
	record(lc.SetContext(ctx))
	wg.Wait()
	// A request may have arrived entirely before SetContext (latched, then
	// replayed by SetContext) or after it (fired directly) — but never both.
	if fired != 1 {
		t.Fatalf("quit fired %d times; want exactly 1", fired)
	}
}
