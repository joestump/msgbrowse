package embed

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/llm"
	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// fakeClient returns a deterministic 2-D vector per input and records how many
// inputs it was asked to embed.
type fakeClient struct {
	calls    int
	embedded int
}

func (f *fakeClient) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	f.calls++
	f.embedded += len(inputs)
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		out[i] = []float32{float32(len(s)), 1}
	}
	return out, nil
}
func (f *fakeClient) Chat(context.Context, llm.ChatRequest) (string, error) { return "", nil }
func (f *fakeClient) Transcribe(context.Context, []byte, string) (string, error) {
	return "", nil
}
func (f *fakeClient) Vision(context.Context, []byte, string, string) (string, error) {
	return "", nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "embed.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seed(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	conv, err := st.UpsertConversation(ctx, source.Signal, "Harper")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ts, sender, body string, sys bool) signal.Message {
		parsed, _ := time.Parse(signal.TimestampLayout, ts)
		return signal.Message{Conversation: "Harper", Timestamp: parsed, TimestampRaw: ts,
			Sender: sender, Body: body, IsSystem: sys}
	}
	msgs := []signal.Message{
		mk("2022-03-01 09:00:00", "Harper", "the lease agreement", false),
		mk("2022-03-01 09:01:00", "Me", "lunch tomorrow", false),
		mk("2022-03-01 09:02:00", "No-Sender", "", true),  // system + empty: skipped
		mk("2022-03-01 09:03:00", "Harper", "   ", false), // whitespace-only: skipped
	}
	if _, err := st.ReplaceConversationMessages(ctx, conv, source.Signal, msgs); err != nil {
		t.Fatal(err)
	}
}

func TestRunEmbedsMissingThenIdempotent(t *testing.T) {
	st := newStore(t)
	seed(t, st)
	ctx := context.Background()
	fc := &fakeClient{}
	opts := Options{EmbedModel: "test-embed", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	sum, err := Run(ctx, st, fc, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Only the two real, non-empty, non-system messages get embedded.
	if sum.Embedded != 2 {
		t.Errorf("embedded = %d, want 2", sum.Embedded)
	}
	if fc.embedded != 2 {
		t.Errorf("client embedded = %d, want 2", fc.embedded)
	}

	// Re-run is a no-op: nothing missing, no client calls.
	fc2 := &fakeClient{}
	sum2, err := Run(ctx, st, fc2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Embedded != 0 || fc2.calls != 0 {
		t.Errorf("re-run embedded %d in %d calls, want 0/0", sum2.Embedded, fc2.calls)
	}
}

func TestRunRespectsBatchSize(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	conv, _ := st.UpsertConversation(ctx, source.Signal, "Big")
	var msgs []signal.Message
	for i := 0; i < 10; i++ {
		parsed, _ := time.Parse(signal.TimestampLayout, "2022-03-01 09:00:00")
		msgs = append(msgs, signal.Message{
			Conversation: "Big", Timestamp: parsed.Add(time.Duration(i) * time.Minute),
			TimestampRaw: "2022-03-01 09:00:00", Sender: "X", Body: padBody(i),
		})
	}
	if _, err := st.ReplaceConversationMessages(ctx, conv, source.Signal, msgs); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{}
	sum, err := Run(ctx, st, fc, Options{EmbedModel: "m", BatchSize: 4, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Embedded != 10 {
		t.Errorf("embedded = %d, want 10", sum.Embedded)
	}
	// 10 messages / batch 4 = 3 batches (4+4+2).
	if sum.Batches != 3 || fc.calls != 3 {
		t.Errorf("batches = %d, calls = %d, want 3/3", sum.Batches, fc.calls)
	}
}

func TestRunNoModel(t *testing.T) {
	st := newStore(t)
	if _, err := Run(context.Background(), st, &fakeClient{}, Options{}); err == nil {
		t.Error("expected error when embed model is unset")
	}
}

// padBody makes each message body unique and non-empty.
func padBody(i int) string {
	return "message body number " + string(rune('a'+i))
}
