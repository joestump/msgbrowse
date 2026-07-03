// Governing: ADR-0018 (token-gated pairing window), SPEC-0011 REQ "Pairing
// Initiation" — single-use token, TTL ≤ 10 minutes on the issuer's clock,
// invalidated on first use / expiry / explicit close / disable, and a
// five-consecutive-failure closure. SPEC-0011 "Replay Resistance" — tokens
// are consumed atomically on first presentation and compared in constant time.
package devices

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

const (
	// MaxTokenTTL is the hard ceiling SPEC-0011 places on a pairing token's
	// lifetime. OpenWindow rejects anything longer.
	MaxTokenTTL = 10 * time.Minute

	// DefaultTokenTTL is the TTL used when the caller passes 0: long enough to
	// fetch a second device and scan a QR, comfortably under the ceiling.
	DefaultTokenTTL = 5 * time.Minute

	// MaxPairingFailures is the consecutive-failure budget: the window closes
	// itself when the count is reached (SPEC-0011 "Brute force closes the
	// window").
	MaxPairingFailures = 5

	// tokenBytes is the entropy of a pairing token before encoding. 32 bytes
	// (256 bits) from crypto/rand makes online guessing within a ≤10-minute,
	// five-attempt window statistically irrelevant.
	tokenBytes = 32
)

// CloseReason says why a pairing window is closed, for logging and for the
// settings UI to surface (a rate-limited closure renders differently from a
// completed pairing).
type CloseReason string

const (
	// CloseNone means the window is still open.
	CloseNone CloseReason = ""
	// CloseConsumed means a pairing completed with the token (success).
	CloseConsumed CloseReason = "consumed"
	// CloseExpired means the TTL elapsed on the issuer's clock.
	CloseExpired CloseReason = "expired"
	// CloseExplicit means the operator closed the window.
	CloseExplicit CloseReason = "closed"
	// CloseDisabled means device sync was disabled while the window was open.
	CloseDisabled CloseReason = "disabled"
	// CloseRateLimited means MaxPairingFailures consecutive failures closed it.
	CloseRateLimited CloseReason = "rate-limited"
)

// Window is one pairing window: a single-use token with a TTL, guarded by a
// mutex so presentation, failure counting, and closure are atomic. A Window
// is not reusable — pairing another device means opening a new Window.
//
// All expiry decisions use the issuing node's own clock (the `now` func),
// never a peer-supplied timestamp, so clock skew on the replica can neither
// extend nor shorten the window (SPEC-0011 design "Clock skew vs token TTL").
type Window struct {
	mu        sync.Mutex
	token     string
	issuedAt  time.Time
	ttl       time.Duration
	failures  int
	closeWhy  CloseReason
	consumed  bool
	now       func() time.Time
	onClosure func(WindowStatus) // optional, fired once under lock on close
}

// WindowOption customizes a Window at open time.
type WindowOption func(*Window)

// WithClock overrides the window's time source (tests).
func WithClock(now func() time.Time) WindowOption {
	return func(w *Window) { w.now = now }
}

// WithCloseHook registers a callback fired exactly once when the window
// closes, with the final status. The listener/settings stories use it to log
// and surface closures (notably rate-limited ones) without polling.
func WithCloseHook(fn func(WindowStatus)) WindowOption {
	return func(w *Window) { w.onClosure = fn }
}

// OpenWindow mints a new pairing window with a fresh random token. ttl == 0
// selects DefaultTokenTTL; a ttl above MaxTokenTTL or below zero is rejected
// rather than clamped, so a misconfigured caller fails loudly instead of
// silently issuing a longer-lived secret than the spec allows.
func OpenWindow(ttl time.Duration, opts ...WindowOption) (*Window, error) {
	switch {
	case ttl == 0:
		ttl = DefaultTokenTTL
	case ttl < 0:
		return nil, fmt.Errorf("devices: pairing window ttl %v is negative", ttl)
	case ttl > MaxTokenTTL:
		return nil, fmt.Errorf("devices: pairing window ttl %v exceeds maximum %v", ttl, MaxTokenTTL)
	}
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("devices: generate pairing token: %w", err)
	}
	w := &Window{
		token: base64.RawURLEncoding.EncodeToString(buf),
		ttl:   ttl,
		now:   time.Now,
	}
	for _, opt := range opts {
		opt(w)
	}
	w.issuedAt = w.now()
	return w, nil
}

// Token returns the window's token for embedding in the pairing payload, or
// "" if the window is no longer open. The token is a secret: it goes into the
// QR/manual payload and nowhere else — never into logs.
func (w *Window) Token() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closedLocked() {
		return ""
	}
	return w.token
}

// ExpiresAt returns the instant the token expires on the issuer's clock.
func (w *Window) ExpiresAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.issuedAt.Add(w.ttl)
}

// Consume presents a token against the window. It is the single atomic
// consumption point: under one lock it checks closure, expiry (issuer's
// clock), and token equality in constant time, then either marks the token
// consumed (success — the window closes) or counts a failure (five
// consecutive failures close the window).
//
// Errors: ErrTokenConsumed on replay after a successful pairing,
// ErrTokenExpired when the TTL has elapsed (the window closes),
// ErrWindowClosed when the window was already closed (explicitly,
// rate-limited, or disabled), ErrTokenInvalid on a mismatch.
func (w *Window) Consume(presented string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.consumed {
		return ErrTokenConsumed
	}
	// Expiry outranks other closure reporting so the caller always gets the
	// most specific error: a token presented after TTL — whether this call is
	// the one that latches the closure or a later one — reports ErrTokenExpired,
	// not a generic closed-window error.
	if w.closeWhy == CloseExpired || (w.closeWhy == CloseNone && w.now().After(w.issuedAt.Add(w.ttl))) {
		w.closeLocked(CloseExpired)
		return ErrTokenExpired
	}
	if w.closeWhy != CloseNone {
		return ErrWindowClosed
	}
	// Constant-time comparison (SPEC-0011 "Replay Resistance"). Both sides are
	// fixed-length encodings of tokenBytes random bytes; ConstantTimeCompare
	// short-circuits only on length, which is public.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(w.token)) != 1 {
		w.failures++
		if w.failures >= MaxPairingFailures {
			w.closeLocked(CloseRateLimited)
		}
		return ErrTokenInvalid
	}
	w.consumed = true
	w.closeLocked(CloseConsumed)
	return nil
}

// Close closes the window explicitly (operator action). Idempotent; a window
// already closed for another reason keeps its original reason.
func (w *Window) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked(CloseExplicit)
}

// Disable closes the window because device sync was disabled. Idempotent.
func (w *Window) Disable() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeLocked(CloseDisabled)
}

// WindowStatus is a point-in-time snapshot for logs, the CLI, and the
// settings UI (which must surface rate-limited closures per SPEC-0011).
type WindowStatus struct {
	Open      bool
	Failures  int
	IssuedAt  time.Time
	ExpiresAt time.Time
	Reason    CloseReason
}

// Status reports the window's current state. Reading the status of an
// expired-but-untouched window reports it closed with CloseExpired (and
// latches that closure), so pollers and Consume agree.
func (w *Window) Status() WindowStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closeWhy == CloseNone && w.now().After(w.issuedAt.Add(w.ttl)) {
		w.closeLocked(CloseExpired)
	}
	return WindowStatus{
		Open:      w.closeWhy == CloseNone,
		Failures:  w.failures,
		IssuedAt:  w.issuedAt,
		ExpiresAt: w.issuedAt.Add(w.ttl),
		Reason:    w.closeWhy,
	}
}

// closedLocked reports whether the window is closed, latching TTL expiry as a
// closure so every path observes the same terminal state. Callers hold w.mu.
func (w *Window) closedLocked() bool {
	if w.closeWhy != CloseNone {
		return true
	}
	if w.now().After(w.issuedAt.Add(w.ttl)) {
		w.closeLocked(CloseExpired)
		return true
	}
	return false
}

// closeLocked records the first closure reason and fires the close hook once.
// Callers hold w.mu.
func (w *Window) closeLocked(reason CloseReason) {
	if w.closeWhy != CloseNone {
		return
	}
	w.closeWhy = reason
	if w.onClosure != nil {
		w.onClosure(WindowStatus{
			Open:      false,
			Failures:  w.failures,
			IssuedAt:  w.issuedAt,
			ExpiresAt: w.issuedAt.Add(w.ttl),
			Reason:    reason,
		})
	}
}
