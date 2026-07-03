// Package shellstate is the desktop shell's lifecycle bookkeeping as a pure,
// headlessly-testable state machine: which context the Wails runtime handed
// us, whether a quit has been requested, and — the part that earned its own
// package — the pre-startup quit latch. A SIGINT/SIGTERM (or embedded-server
// death) landing in the window between embedded.Start and Wails OnStartup
// used to be silently dropped because the runtime context was still nil;
// the latch records the request and SetContext replays it exactly once
// (startup-race hardening from #114's review, issue #118).
//
// The package is pure Go with no Wails import so `make desktop-test` proves
// the lifecycle logic under CGO_ENABLED=0; the tagged shell wiring next door
// binds the returned contexts to wailsruntime calls.
//
// Governing: SPEC-0010 REQ "Menubar residency" (explicit quit only), REQ
// "Graceful shutdown" (every quit path funnels into one context cancel).
package shellstate

import (
	"context"
	"sync"
)

// Lifecycle tracks the shell's quit state across the Wails app lifecycle.
// The zero value is ready to use. Methods that may trigger a runtime quit
// return the context to fire it on (nil means "do nothing"), so the caller
// performs the runtime call outside the lock and the state transitions stay
// testable without a GUI.
type Lifecycle struct {
	mu            sync.Mutex
	ctx           context.Context
	quitRequested bool // an explicit quit (tray/menu/signal/server death) is in flight
	quitFired     bool // the runtime quit has been issued (or shutdown has begun)
}

// SetContext installs the Wails runtime context at OnStartup. It returns a
// non-nil context exactly when a quit was requested before startup — the
// caller must fire the runtime quit on it. This is the latch replay: the
// pre-startup request was recorded instead of dropped, and it fires the
// moment a context exists.
func (l *Lifecycle) SetContext(ctx context.Context) (fire context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ctx = ctx
	if l.quitRequested && !l.quitFired {
		l.quitFired = true
		return ctx
	}
	return nil
}

// RequestQuit records an explicit quit request and returns the context to
// fire the runtime quit on. It returns nil when the runtime context does not
// exist yet (the request is latched; SetContext replays it) or when a quit
// has already fired (idempotence — every quit path may call this safely).
func (l *Lifecycle) RequestQuit() (fire context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.quitRequested = true
	if l.ctx == nil || l.quitFired {
		return nil
	}
	l.quitFired = true
	return l.ctx
}

// MarkShutdown records OnShutdown: the runtime is tearing down (possibly via
// a quit path that never crossed RequestQuit, e.g. Cmd+Q on macOS), so any
// later RequestQuit must be a no-op.
func (l *Lifecycle) MarkShutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.quitRequested = true
	l.quitFired = true
}

// Context returns the Wails runtime context, or nil before OnStartup. Tray
// actions use it to no-op gracefully in the sliver of time before the
// runtime exists.
func (l *Lifecycle) Context() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ctx
}

// QuitRequested reports whether an explicit quit is in flight — the shell is
// resident-until-quit (SPEC-0010 "Menubar residency"), so window-visibility
// decisions consult this rather than assuming close means quit.
func (l *Lifecycle) QuitRequested() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.quitRequested
}
