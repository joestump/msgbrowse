package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/joestump/sigbrowse/internal/signal"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func msg(conv, ts, sender, body string, atts []signal.Attachment, links []signal.Link) signal.Message {
	parsed, _ := time.Parse(signal.TimestampLayout, ts)
	return signal.Message{
		Conversation: conv, Timestamp: parsed, TimestampRaw: ts,
		Sender: sender, Body: body, Attachments: atts, Links: links,
	}
}

func TestUpsertConversationIdempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id1, err := st.UpsertConversation(ctx, "Harper")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := st.UpsertConversation(ctx, "Harper")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("UpsertConversation not idempotent: %d != %d", id1, id2)
	}
}

func TestReplaceConversationMessagesAndFTS(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, err := st.UpsertConversation(ctx, "Harper")
	if err != nil {
		t.Fatal(err)
	}

	msgs := []signal.Message{
		msg("Harper", "2022-03-01 09:00:00", "Harper", "talk about the lease today",
			[]signal.Attachment{{Kind: signal.KindFile, RelPath: "media/lease.pdf", OriginalName: "lease.pdf"}},
			[]signal.Link{{URL: "https://example.com/x"}}),
		msg("Harper", "2022-03-01 09:01:00", "Me", "sounds good", nil, nil),
	}
	added, err := st.ReplaceConversationMessages(ctx, id, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	// FTS index is populated via triggers.
	if n := ftsCount(t, st, "lease"); n != 1 {
		t.Errorf("fts match 'lease' = %d, want 1", n)
	}

	// Attachments and links written.
	if n := scalar(t, st, `SELECT count(*) FROM attachments`); n != 1 {
		t.Errorf("attachments = %d, want 1", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM links`); n != 1 {
		t.Errorf("links = %d, want 1", n)
	}
	if dom := scalarStr(t, st, `SELECT domain FROM links LIMIT 1`); dom != "example.com" {
		t.Errorf("link domain = %q, want example.com", dom)
	}

	// Replacing with a smaller set cascades deletes and re-syncs FTS.
	added, err = st.ReplaceConversationMessages(ctx, id,
		[]signal.Message{msg("Harper", "2022-03-01 09:02:00", "Me", "new content only", nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	if n := ftsCount(t, st, "lease"); n != 0 {
		t.Errorf("after replace, fts 'lease' = %d, want 0 (stale index)", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM attachments`); n != 0 {
		t.Errorf("after replace, attachments = %d, want 0 (cascade)", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM links`); n != 0 {
		t.Errorf("after replace, links = %d, want 0 (cascade)", n)
	}
}

func TestIngestStateRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	id, _ := st.UpsertConversation(ctx, "Harper")

	if got, err := st.GetIngestState(ctx, id); err != nil || got != nil {
		t.Fatalf("expected no state, got %v err %v", got, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	want := IngestState{
		ConversationID: id, RelPath: "export/Harper/chat.md",
		MTimeUnix: 123, SizeBytes: 456, ContentHash: "abc",
		MessageCount: 7, LastIngestedAt: now,
	}
	if err := st.SetIngestState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetIngestState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash != "abc" || got.MessageCount != 7 || got.SizeBytes != 456 || !got.LastIngestedAt.Equal(now) {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Upsert path.
	want.ContentHash = "def"
	want.MessageCount = 9
	if err := st.SetIngestState(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetIngestState(ctx, id)
	if got.ContentHash != "def" || got.MessageCount != 9 {
		t.Errorf("upsert failed: %+v", got)
	}
}

func TestReplaceSnapshots(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	snaps := []Snapshot{
		{Filename: "db-20240101-090000.tar", TakenAt: time.Unix(1704099600, 0).UTC(), SizeBytes: 10, Tier: "yearly"},
		{Filename: "db-20250601-090000.tar", TakenAt: time.Unix(1748768400, 0).UTC(), SizeBytes: 20, Tier: "monthly"},
	}
	if err := st.ReplaceSnapshots(ctx, snaps); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, `SELECT count(*) FROM snapshots`); n != 2 {
		t.Errorf("snapshots = %d, want 2", n)
	}
	// Replace is a full swap.
	if err := st.ReplaceSnapshots(ctx, snaps[:1]); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, st, `SELECT count(*) FROM snapshots`); n != 1 {
		t.Errorf("after replace, snapshots = %d, want 1", n)
	}
}

func TestRecordIngestRun(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	id, err := st.RecordIngestRun(ctx, IngestRun{
		StartedAt: now, FinishedAt: now.Add(time.Second), DurationMS: 1000,
		ConversationsScanned: 2, MessagesTotal: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Error("expected non-zero run id")
	}
}

// helpers

func ftsCount(t *testing.T, st *Store, term string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?`, term).Scan(&n); err != nil {
		t.Fatalf("fts query: %v", err)
	}
	return n
}

func scalar(t *testing.T, st *Store, q string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func scalarStr(t *testing.T, st *Store, q string) string {
	t.Helper()
	var s string
	if err := st.DB().QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return s
}
