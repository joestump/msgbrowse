package embedsvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/llm"
	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// fakeClient is a scriptable llm.Client: Embed can block on gate (to hold a
// run open for the singleton/cancellation tests) and fail on demand. It only
// ever sees synthetic fixture text.
type fakeClient struct {
	gate     chan struct{} // when non-nil, Embed waits for it (or ctx) before returning
	err      error
	calls    atomic.Int32
	embedded atomic.Int32
}

func (f *fakeClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	f.calls.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	f.embedded.Add(int32(len(inputs)))
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		out[i] = []float32{float32(len(s)), 1}
	}
	return out, nil
}
func (f *fakeClient) Chat(context.Context, llm.ChatRequest) (string, error)      { return "", nil }
func (f *fakeClient) Transcribe(context.Context, []byte, string) (string, error) { return "", nil }
func (f *fakeClient) Vision(context.Context, []byte, string, string) (string, error) {
	return "", nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "embedsvc.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seed inserts n synthetic embeddable messages.
func seed(t *testing.T, st *store.Store, n int) {
	t.Helper()
	ctx := context.Background()
	conv, err := st.UpsertConversation(ctx, source.Signal, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := time.Parse(signal.TimestampLayout, "2022-03-01 09:00:00")
	var msgs []signal.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs, signal.Message{
			Conversation: "Fixture", Timestamp: base.Add(time.Duration(i) * time.Minute),
			TimestampRaw: "2022-03-01 09:00:00", Sender: "X",
			Body: "synthetic fixture line " + string(rune('a'+i)),
		})
	}
	if _, err := st.ReplaceConversationMessages(ctx, conv, source.Signal, msgs); err != nil {
		t.Fatal(err)
	}
}

func newJob(t *testing.T, st *store.Store, client llm.Client, model string) *Job {
	t.Helper()
	j := New(st, client, func() string { return model }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(j.Shutdown)
	return j
}

// waitState polls until the job reaches a terminal-or-wanted state.
func waitState(t *testing.T, j *Job, want State) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := j.Status(); s.State == want {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job never reached state %q (last: %+v)", want, j.Status())
	return Status{}
}

// TestKickUnconfiguredNoop: with no embed model configured, Kick starts
// nothing — no egress, no state change (the deliberate-egress posture).
func TestKickUnconfiguredNoop(t *testing.T) {
	st := newStore(t)
	seed(t, st, 2)
	fc := &fakeClient{}
	j := newJob(t, st, fc, "")

	if j.Kick() {
		t.Fatal("Kick with no embed model should be a no-op")
	}
	if s := j.Status(); s.State != StateIdle {
		t.Fatalf("state = %q, want idle", s.State)
	}
	if fc.calls.Load() != 0 {
		t.Fatalf("client called %d times, want 0", fc.calls.Load())
	}
}

// TestKickSingletonGuard: a second Kick while a run is in flight is a no-op —
// never two concurrent runs. After the run completes, Kick works again.
func TestKickSingletonGuard(t *testing.T) {
	st := newStore(t)
	seed(t, st, 3)
	gate := make(chan struct{})
	fc := &fakeClient{gate: gate}
	j := newJob(t, st, fc, "test-embed")

	if !j.Kick() {
		t.Fatal("first Kick should start a run")
	}
	waitState(t, j, StateRunning)
	for i := 0; i < 3; i++ {
		if j.Kick() {
			t.Fatal("Kick while running must be a no-op (singleton guard)")
		}
	}

	close(gate) // release the blocked Embed
	s := waitState(t, j, StateDone)
	if s.Processed != 3 {
		t.Fatalf("processed = %d, want 3", s.Processed)
	}
	if int(fc.embedded.Load()) != 3 {
		t.Fatalf("client embedded %d, want 3", fc.embedded.Load())
	}

	// Idle again: a fresh Kick is accepted (and completes instantly — nothing
	// left to embed, no client calls).
	before := fc.calls.Load()
	if !j.Kick() {
		t.Fatal("Kick after completion should start a run")
	}
	waitState(t, j, StateDone)
	if fc.calls.Load() != before {
		t.Fatalf("up-to-date run made %d client calls, want 0", fc.calls.Load()-before)
	}
}

// TestRunReportsProgressAndNotes: the running status carries live counts, and
// a completed run leaves count/timing notes (never content) for the Logs page.
func TestRunReportsProgressAndNotes(t *testing.T) {
	st := newStore(t)
	seed(t, st, 4)
	fc := &fakeClient{}
	j := newJob(t, st, fc, "test-embed")

	if !j.Kick() {
		t.Fatal("Kick should start")
	}
	s := waitState(t, j, StateDone)
	if s.Processed != 4 || s.Total != 4 {
		t.Fatalf("final progress = %d/%d, want 4/4", s.Processed, s.Total)
	}
	if s.Model != "test-embed" {
		t.Fatalf("model = %q", s.Model)
	}

	notes := j.Notes()
	if len(notes) < 2 {
		t.Fatalf("notes = %d, want at least start + completion", len(notes))
	}
	for _, n := range notes {
		if n.IsError() {
			t.Errorf("unexpected error note: %s", n.Message)
		}
		// Guard the no-content rule for the fixture bodies.
		if contains(n.Message, "synthetic fixture line") {
			t.Errorf("note leaked message content: %s", n.Message)
		}
	}
}

// TestRunFailure: a provider error lands in StateFailed with the error
// recorded (status + an error note), and a later Kick may retry.
func TestRunFailure(t *testing.T) {
	st := newStore(t)
	seed(t, st, 2)
	provErr := errors.New("connection refused")
	fc := &fakeClient{err: provErr}
	j := newJob(t, st, fc, "test-embed")

	if !j.Kick() {
		t.Fatal("Kick should start")
	}
	s := waitState(t, j, StateFailed)
	if s.Err == nil || !contains(s.ErrText(), "connection refused") {
		t.Fatalf("failed status err = %v", s.Err)
	}
	var sawError bool
	for _, n := range j.Notes() {
		if n.IsError() {
			sawError = true
		}
	}
	if !sawError {
		t.Error("failed run left no error note for the Logs page")
	}

	// Retry is accepted once idle.
	fc.err = nil
	if !j.Kick() {
		t.Fatal("Kick after failure should start a retry")
	}
	waitState(t, j, StateDone)
}

// TestShutdownCancelsCleanly: Shutdown while a run is blocked mid-batch stops
// it as Cancelled (not Failed) — progress is persisted per batch, so resuming
// later just picks up the remainder — and a post-shutdown Kick starts nothing.
func TestShutdownCancelsCleanly(t *testing.T) {
	st := newStore(t)
	seed(t, st, 2)
	gate := make(chan struct{}) // never closed: Embed blocks until ctx cancels
	fc := &fakeClient{gate: gate}
	j := New(st, fc, func() string { return "test-embed" }, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if !j.Kick() {
		t.Fatal("Kick should start")
	}
	waitState(t, j, StateRunning)
	j.Shutdown() // cancels the run context and waits for the goroutine

	if s := j.Status(); s.State != StateCancelled {
		t.Fatalf("state after shutdown = %q, want cancelled", s.State)
	}
	if j.Kick() {
		t.Fatal("Kick after Shutdown should be a no-op")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
