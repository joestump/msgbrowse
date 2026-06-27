package imessage

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/signal"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "imsg.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func runImport(t *testing.T, st *store.Store, full bool) store.IngestRun {
	t.Helper()
	run, err := Run(context.Background(), st, Options{
		ArchiveRoot: filepath.Join("..", "..", "testdata", "imessage"),
		Full:        full,
		Now:         func() time.Time { return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC) },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return run
}

func TestImportFixture(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	run := runImport(t, st, false)

	if run.ConversationsScanned != 2 || run.ConversationsChanged != 2 {
		t.Errorf("scanned/changed = %d/%d, want 2/2", run.ConversationsScanned, run.ConversationsChanged)
	}
	if run.Source != source.IMessage {
		t.Errorf("run source = %q", run.Source)
	}

	// Both conversations exist, tagged imessage.
	convs, err := st.ListConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range convs {
		names[c.Name] = true
	}
	if !names["MJ"] || !names["Group Trip"] {
		t.Errorf("conversations = %v, want MJ + Group Trip", names)
	}

	// MJ has an image attachment; Group Trip has a file (pdf) + a link.
	if n := scalar(t, st, `SELECT count(*) FROM attachments WHERE kind='image'`); n != 1 {
		t.Errorf("image attachments = %d, want 1", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM attachments WHERE kind='file'`); n != 1 {
		t.Errorf("file attachments = %d, want 1", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM links`); n < 2 {
		t.Errorf("links = %d, want >= 2", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE source != 'imessage'`); n != 0 {
		t.Errorf("found %d non-imessage messages", n)
	}
}

func TestImportIdempotent(t *testing.T) {
	st := newStore(t)
	runImport(t, st, false)
	again := runImport(t, st, false)
	if again.ConversationsChanged != 0 {
		t.Errorf("re-import changed %d conversations, want 0 (incremental)", again.ConversationsChanged)
	}
}

// TestCrossSourceNoCollision confirms an iMessage conversation and a Signal
// conversation with the SAME name and an identical (timestamp, sender, body)
// message both persist — the source-namespaced hash prevents a global
// messages.hash collision.
func TestCrossSourceNoCollision(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// Signal "MJ" with one message.
	sigConv, err := st.UpsertConversation(ctx, source.Signal, "MJ")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := time.Parse(signal.TimestampLayout, "2020-05-20 09:00:00")
	sigMsg := signal.Message{Conversation: "MJ", Timestamp: parsed, TimestampRaw: "2020-05-20 09:00:00", Sender: "MJ", Body: "ping"}
	if _, err := st.ReplaceConversationMessages(ctx, sigConv, source.Signal, []signal.Message{sigMsg}); err != nil {
		t.Fatal(err)
	}

	// iMessage "MJ" with a message whose (conv, sender, body) match — only the
	// raw timestamp text differs by format, but even identical text must not
	// collide because the hash is source-namespaced.
	imConv, err := st.UpsertConversation(ctx, source.IMessage, "MJ")
	if err != nil {
		t.Fatal(err)
	}
	imMsg := signal.Message{Conversation: "MJ", Timestamp: parsed, TimestampRaw: "2020-05-20 09:00:00", Sender: "MJ", Body: "ping"}
	if _, err := st.ReplaceConversationMessages(ctx, imConv, source.IMessage, []signal.Message{imMsg}); err != nil {
		t.Fatalf("imessage insert collided with signal: %v", err)
	}

	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE body='ping'`); n != 2 {
		t.Errorf("messages with body 'ping' = %d, want 2 (one per source)", n)
	}
	if sigConv == imConv {
		t.Error("expected distinct conversations across sources")
	}
}

func scalar(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}
