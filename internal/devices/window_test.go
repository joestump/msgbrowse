// Governing: SPEC-0011 REQ "Pairing Initiation" — token lifecycle tests:
// issue/consume/expire/replay-reject and the five-failure window closure.
package devices

import (
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable time source for driving TTL expiry.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestOpenWindowTTLBounds(t *testing.T) {
	tests := []struct {
		name    string
		ttl     time.Duration
		wantErr bool
		wantTTL time.Duration
	}{
		{name: "zero selects default", ttl: 0, wantTTL: DefaultTokenTTL},
		{name: "explicit under cap", ttl: 2 * time.Minute, wantTTL: 2 * time.Minute},
		{name: "exactly the cap", ttl: MaxTokenTTL, wantTTL: MaxTokenTTL},
		{name: "over the cap rejected", ttl: MaxTokenTTL + time.Second, wantErr: true},
		{name: "negative rejected", ttl: -time.Minute, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			w, err := OpenWindow(tt.ttl, WithClock(clock.Now))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("OpenWindow(%v) succeeded, want error", tt.ttl)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenWindow(%v): %v", tt.ttl, err)
			}
			if got := w.ExpiresAt().Sub(clock.Now()); got != tt.wantTTL {
				t.Errorf("effective ttl = %v, want %v", got, tt.wantTTL)
			}
		})
	}
}

func TestWindowTokenProperties(t *testing.T) {
	w, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	tok := w.Token()
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if len(raw) != tokenBytes {
		t.Errorf("token entropy = %d bytes, want %d", len(raw), tokenBytes)
	}
	// Two windows never share a token.
	w2, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Token() == tok {
		t.Error("two windows issued the same token")
	}
}

// TestWindowLifecycle is the table-driven token lifecycle:
// issue → consume / expire / replay / mismatch / close / disable.
func TestWindowLifecycle(t *testing.T) {
	tests := []struct {
		name string
		// arrange mutates the window (and clock) before the final Consume.
		arrange func(t *testing.T, w *Window, clock *fakeClock)
		// present selects the token to present ("" = the real token).
		present    func(w *Window) string
		wantErr    error
		wantReason CloseReason
	}{
		{
			name:       "valid token consumes and closes the window",
			arrange:    func(t *testing.T, w *Window, c *fakeClock) {},
			wantErr:    nil,
			wantReason: CloseConsumed,
		},
		{
			name: "replay after success is rejected",
			arrange: func(t *testing.T, w *Window, c *fakeClock) {
				if err := w.Consume(w.Token()); err != nil {
					t.Fatalf("first consume: %v", err)
				}
			},
			wantErr:    ErrTokenConsumed,
			wantReason: CloseConsumed,
		},
		{
			name: "expired token rejected and window closed",
			arrange: func(t *testing.T, w *Window, c *fakeClock) {
				c.Advance(DefaultTokenTTL + time.Second)
			},
			wantErr:    ErrTokenExpired,
			wantReason: CloseExpired,
		},
		{
			name: "second presentation after expiry still reports expired",
			arrange: func(t *testing.T, w *Window, c *fakeClock) {
				c.Advance(DefaultTokenTTL + time.Second)
				if err := w.Consume(w.Token()); !errors.Is(err, ErrTokenExpired) {
					t.Fatalf("first post-expiry consume = %v, want ErrTokenExpired", err)
				}
			},
			wantErr:    ErrTokenExpired,
			wantReason: CloseExpired,
		},
		{
			name:       "mismatched token rejected",
			arrange:    func(t *testing.T, w *Window, c *fakeClock) {},
			present:    func(w *Window) string { return "not-the-token" },
			wantErr:    ErrTokenInvalid,
			wantReason: CloseNone,
		},
		{
			name: "explicitly closed window refuses even the right token",
			arrange: func(t *testing.T, w *Window, c *fakeClock) {
				w.Close()
			},
			wantErr:    ErrWindowClosed,
			wantReason: CloseExplicit,
		},
		{
			name: "disable closes the window",
			arrange: func(t *testing.T, w *Window, c *fakeClock) {
				w.Disable()
			},
			wantErr:    ErrWindowClosed,
			wantReason: CloseDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			w, err := OpenWindow(0, WithClock(clock.Now))
			if err != nil {
				t.Fatal(err)
			}
			realToken := w.Token() // capture before closure blanks it
			tt.arrange(t, w, clock)

			presented := realToken
			if tt.present != nil {
				presented = tt.present(w)
			}
			err = w.Consume(presented)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Consume = %v, want %v", err, tt.wantErr)
			}
			if got := w.Status().Reason; got != tt.wantReason {
				t.Errorf("close reason = %q, want %q", got, tt.wantReason)
			}
		})
	}
}

// TestWindowFiveFailureClosure covers SPEC-0011's "Brute force closes the
// window": five consecutive failures close the window, after which even the
// correct token is refused until a NEW window opens.
func TestWindowFiveFailureClosure(t *testing.T) {
	var closed []WindowStatus
	w, err := OpenWindow(0, WithCloseHook(func(st WindowStatus) { closed = append(closed, st) }))
	if err != nil {
		t.Fatal(err)
	}
	realToken := w.Token()

	for i := 1; i <= MaxPairingFailures; i++ {
		if err := w.Consume("wrong-token"); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("failure %d: Consume = %v, want ErrTokenInvalid", i, err)
		}
		st := w.Status()
		if st.Failures != i {
			t.Fatalf("failure %d: counted %d", i, st.Failures)
		}
		wantOpen := i < MaxPairingFailures
		if st.Open != wantOpen {
			t.Fatalf("failure %d: open = %v, want %v", i, st.Open, wantOpen)
		}
	}

	// The window is rate-limit closed and surfaced as such.
	if got := w.Status().Reason; got != CloseRateLimited {
		t.Errorf("close reason = %q, want %q", got, CloseRateLimited)
	}
	if len(closed) != 1 || closed[0].Reason != CloseRateLimited || closed[0].Failures != MaxPairingFailures {
		t.Errorf("close hook = %+v, want one rate-limited closure with %d failures", closed, MaxPairingFailures)
	}

	// Even the REAL token is now refused: pairing requires a new window.
	if err := w.Consume(realToken); !errors.Is(err, ErrWindowClosed) {
		t.Errorf("post-closure Consume(real token) = %v, want ErrWindowClosed", err)
	}
	// Token is no longer handed out for payload building.
	if w.Token() != "" {
		t.Error("closed window still exposes its token")
	}

	// A fresh window pairs normally — closure is per-window.
	w2, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Consume(w2.Token()); err != nil {
		t.Errorf("fresh window Consume = %v, want success", err)
	}
}

// TestWindowAtomicConsumption races many goroutines presenting the same valid
// token: exactly one may win (single-use is atomic), everyone else gets
// ErrTokenConsumed. Run with -race in CI per SPEC-0011 "Concurrency Safety".
func TestWindowAtomicConsumption(t *testing.T) {
	w, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	tok := w.Token()

	const goroutines = 32
	var wg sync.WaitGroup
	results := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- w.Consume(tok)
		}()
	}
	wg.Wait()
	close(results)

	var wins, replays, other int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrTokenConsumed):
			replays++
		default:
			other++
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1", wins)
	}
	if replays != goroutines-1 {
		t.Errorf("replay rejections = %d, want %d", replays, goroutines-1)
	}
	if other != 0 {
		t.Errorf("unexpected errors: %d", other)
	}
}

// TestWindowConstantTimeComparison pins the comparison primitive: Consume
// must depend only on byte equality (crypto/subtle), never on prefix
// structure — a same-length token differing in the last byte and one
// differing in the first byte are both plain mismatches, and neither
// perturbs single-use bookkeeping differently than the other.
func TestWindowConstantTimeComparison(t *testing.T) {
	w, err := OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	tok := w.Token()

	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}

	for _, presented := range []string{
		flip(tok, 0),          // differs at the first byte
		flip(tok, len(tok)-1), // differs at the last byte
		tok[:len(tok)-1],      // shorter (length is public; still a mismatch)
		tok + "x",             // longer
	} {
		if err := w.Consume(presented); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("Consume(%q) = %v, want ErrTokenInvalid", presented, err)
		}
	}
	// Four mismatches counted, one budget slot left: real token still works.
	if got := w.Status().Failures; got != 4 {
		t.Fatalf("failures = %d, want 4", got)
	}
	if err := w.Consume(tok); err != nil {
		t.Errorf("Consume(real token) = %v, want success", err)
	}
}
