package ingest

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joestump/sigbrowse/internal/store"
)

// fixedNow is the reference time used for deterministic snapshot tier tests.
var fixedNow = time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)

func testOptions(root string) Options {
	return Options{
		ArchiveRoot: root,
		Now:         func() time.Time { return fixedNow },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// copyFixture copies the read-only testdata archive into a temp dir so tests can
// freely mutate it (e.g. to exercise incremental re-ingest) without touching the
// committed fixture.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "archive")
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ingest.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRunEndToEnd(t *testing.T) {
	root := copyFixture(t)
	st := newStore(t)
	ctx := context.Background()

	run, err := Run(ctx, st, testOptions(root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if run.ConversationsScanned != 2 {
		t.Errorf("scanned = %d, want 2", run.ConversationsScanned)
	}
	if run.ConversationsChanged != 2 {
		t.Errorf("changed = %d, want 2", run.ConversationsChanged)
	}
	if run.MessagesTotal != 11 {
		t.Errorf("messages total = %d, want 11", run.MessagesTotal)
	}
	if run.SkippedLines != 0 {
		t.Errorf("skipped lines = %d, want 0", run.SkippedLines)
	}

	// Attachments: cabin.jpg + lease.pdf + sunset.png.
	if n := count(t, st, `SELECT count(*) FROM attachments`); n != 3 {
		t.Errorf("attachments = %d, want 3", n)
	}
	if n := count(t, st, `SELECT count(*) FROM attachments WHERE kind='image'`); n != 2 {
		t.Errorf("image attachments = %d, want 2", n)
	}
	// Links: maps.example.com + yelp.com (bare and markdown deduped).
	if n := count(t, st, `SELECT count(*) FROM links`); n != 2 {
		t.Errorf("links = %d, want 2", n)
	}
	// One No-Sender system message in Harper.
	if n := count(t, st, `SELECT count(*) FROM messages WHERE is_system=1`); n != 1 {
		t.Errorf("system messages = %d, want 1", n)
	}
	// Multi-line body preserved.
	body := str(t, st, `SELECT body FROM messages WHERE body LIKE 'a few notes%'`)
	if body != "a few notes on\nthe lease terms\nspanning multiple lines" {
		t.Errorf("multi-line body = %q", body)
	}
	// FTS works end to end.
	if n := count(t, st, `SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'lease'`); n < 1 {
		t.Errorf("fts 'lease' = %d, want >=1", n)
	}

	// Snapshots: 3 valid tars (README.txt ignored), with expected tiers.
	if run.SnapshotsSeen != 3 {
		t.Errorf("snapshots seen = %d, want 3", run.SnapshotsSeen)
	}
	tier := func(fn string) string { return str(t, st, `SELECT tier FROM snapshots WHERE filename=?`, fn) }
	if got := tier("db-20260620-090000.tar"); got != "daily" {
		t.Errorf("recent snapshot tier = %q, want daily", got)
	}
	if got := tier("db-20250601-090000.tar"); got != "monthly" {
		t.Errorf("mid snapshot tier = %q, want monthly", got)
	}
	if got := tier("db-20230101-090000.tar"); got != "yearly" {
		t.Errorf("old snapshot tier = %q, want yearly", got)
	}
}

func TestRunIncrementalIdempotent(t *testing.T) {
	root := copyFixture(t)
	st := newStore(t)
	ctx := context.Background()

	if _, err := Run(ctx, st, testOptions(root)); err != nil {
		t.Fatal(err)
	}
	// Second run with nothing changed: no conversation re-parsed.
	run2, err := Run(ctx, st, testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if run2.ConversationsChanged != 0 {
		t.Errorf("second run changed = %d, want 0", run2.ConversationsChanged)
	}
	if run2.MessagesAdded != 0 {
		t.Errorf("second run added = %d, want 0", run2.MessagesAdded)
	}
	if run2.MessagesTotal != 11 {
		t.Errorf("second run total = %d, want 11 (no duplication)", run2.MessagesTotal)
	}
}

func TestRunDetectsChangedConversation(t *testing.T) {
	root := copyFixture(t)
	st := newStore(t)
	ctx := context.Background()

	if _, err := Run(ctx, st, testOptions(root)); err != nil {
		t.Fatal(err)
	}

	// Append a new message to Harper's chat.md (the daily merge behavior).
	harper := filepath.Join(root, "export", "Harper", "chat.md")
	f, err := os.OpenFile(harper, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[2022-03-01 10:00:00] Harper: one more thing\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	run, err := Run(ctx, st, testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if run.ConversationsChanged != 1 {
		t.Errorf("changed = %d, want 1 (only Harper)", run.ConversationsChanged)
	}
	if run.MessagesTotal != 12 {
		t.Errorf("messages total = %d, want 12", run.MessagesTotal)
	}
}

func TestRunFullForcesReparse(t *testing.T) {
	root := copyFixture(t)
	st := newStore(t)
	ctx := context.Background()

	if _, err := Run(ctx, st, testOptions(root)); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(root)
	opts.Full = true
	run, err := Run(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if run.ConversationsChanged != 2 {
		t.Errorf("full run changed = %d, want 2", run.ConversationsChanged)
	}
	if run.MessagesTotal != 11 {
		t.Errorf("full run total = %d, want 11 (idempotent content)", run.MessagesTotal)
	}
}

// helpers

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func str(t *testing.T, st *store.Store, q string, args ...any) string {
	t.Helper()
	var s string
	if err := st.DB().QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return s
}
