package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "wa.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func runImport(t *testing.T, st *store.Store, full bool) store.IngestRun {
	t.Helper()
	run, err := Run(context.Background(), st, Options{
		ArchiveRoot: "testdata",
		Full:        full,
		Now:         func() time.Time { return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC) },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Location:    time.UTC,
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

	if run.Source != source.WhatsApp {
		t.Errorf("run source = %q", run.Source)
	}
	// 6 chats in the fixture, one malformed at the chat level.
	if run.ConversationsScanned != 5 || run.ConversationsChanged != 5 {
		t.Errorf("scanned/changed = %d/%d, want 5/5", run.ConversationsScanned, run.ConversationsChanged)
	}
	// 8 + 6 + 1 + 1 + 2 messages survive; 4 malformed entries are skip-logged.
	if run.MessagesAdded != 18 {
		t.Errorf("messages added = %d, want 18", run.MessagesAdded)
	}
	if run.SkippedLines != 4 {
		t.Errorf("skipped entries = %d, want 4", run.SkippedLines)
	}
	if run.Errors != 0 {
		t.Errorf("errors = %d, want 0", run.Errors)
	}

	convs, err := st.ListConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, c := range convs {
		names[c.Name] = c.Source
	}
	for _, want := range []string{"Ada Fixture", "Ada Fixture (15550005555)", "Fixture Crew", "15550004444", "Abs Path Fixture"} {
		if names[want] != source.WhatsApp {
			t.Errorf("conversation %q source = %q, want whatsapp", want, names[want])
		}
	}

	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE source != 'whatsapp'`); n != 0 {
		t.Errorf("found %d non-whatsapp messages", n)
	}
	// REQ-0009-004 in storage: every ts is canonical (zero non-canonical rows).
	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE ts NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9]'`); n != 0 {
		t.Errorf("%d stored timestamps are non-canonical", n)
	}
	// REQ-0009-005 in storage: reactions land in the reactions table, never bodies.
	if n := scalar(t, st, `SELECT count(*) FROM reactions`); n != 3 {
		t.Errorf("reactions = %d, want 3", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE body LIKE '%👍%' OR body LIKE '%🎉%' OR body LIKE '%❤️%'`); n != 0 {
		t.Errorf("%d bodies carry reaction emoji", n)
	}
	// Missing media keeps its chip-fallback attachment row.
	if n := scalar(t, st, `SELECT count(*) FROM attachments WHERE original_name = 'The media is missing'`); n != 1 {
		t.Errorf("missing-media attachments = %d, want 1", n)
	}
	// vCard anchors became attachments, not body markup.
	if n := scalar(t, st, `SELECT count(*) FROM attachments WHERE rel_path LIKE '%.vcf'`); n != 2 {
		t.Errorf("vcf attachments = %d, want 2", n)
	}
	if n := scalar(t, st, `SELECT count(*) FROM messages WHERE body LIKE '%<a href%' OR body LIKE '%<br>%'`); n != 0 {
		t.Errorf("%d bodies contain exporter markup", n)
	}
	// No absolute attachment paths are ever stored.
	if n := scalar(t, st, `SELECT count(*) FROM attachments WHERE rel_path LIKE '/%'`); n != 0 {
		t.Errorf("%d attachments stored absolute paths", n)
	}
}

// TestImportIdempotent asserts SPEC-0001 semantics: an unchanged export is a
// no-op on re-import (0 conversations changed, reactions not duplicated).
func TestImportIdempotent(t *testing.T) {
	st := newStore(t)
	runImport(t, st, false)
	before := scalar(t, st, `SELECT count(*) FROM reactions`)

	again := runImport(t, st, false)
	if again.ConversationsChanged != 0 {
		t.Errorf("re-import changed %d conversations, want 0 (incremental)", again.ConversationsChanged)
	}
	if again.MessagesAdded != 0 {
		t.Errorf("re-import added %d messages, want 0", again.MessagesAdded)
	}
	if after := scalar(t, st, `SELECT count(*) FROM reactions`); after != before {
		t.Errorf("reactions %d → %d after re-import, want unchanged", before, after)
	}

	// --full forces a rewrite but stays idempotent (same rows, no dupes).
	msgs := scalar(t, st, `SELECT count(*) FROM messages`)
	full := runImport(t, st, true)
	if full.ConversationsChanged != 5 {
		t.Errorf("full re-import changed %d conversations, want 5", full.ConversationsChanged)
	}
	if after := scalar(t, st, `SELECT count(*) FROM messages`); after != msgs {
		t.Errorf("messages %d → %d after full re-import, want unchanged", msgs, after)
	}
	if after := scalar(t, st, `SELECT count(*) FROM reactions`); after != before {
		t.Errorf("reactions %d → %d after full re-import, want unchanged", before, after)
	}
}

// TestCrossSourceIdentityMerge verifies REQ-0009-007's contact half at the
// fixture level: a phone-named WhatsApp conversation (the fixture's
// 15550004444@lid chat, imported by Run) participates in the phone-keyed
// contact_identifiers machinery, so once its number is unified with an
// existing iMessage contact — the deliberate contacts-page action, ADR-0003 —
// both conversations surface each other's handles as identifier chips.
func TestCrossSourceIdentityMerge(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	runImport(t, st, false)

	// The fixture's nameless @lid chat imports under its JID local part — the
	// bare phone number — which is what keys the merge.
	wa, err := st.GetConversation(ctx, "15550004444")
	if err != nil || wa == nil {
		t.Fatalf("whatsapp conversation missing: %v", err)
	}
	if wa.Source != source.WhatsApp {
		t.Fatalf("conversation source = %q, want whatsapp", wa.Source)
	}
	// Pre-merge: the auto-created contact carries only the conversation's own
	// (whatsapp, 15550004444) identity, so no chips render — and, per
	// ADR-0003, import alone never silently merged it onto anyone.
	if ids, err := st.ConversationIdentifiers(ctx, wa.ID); err != nil || len(ids) != 0 {
		t.Fatalf("pre-merge identifiers = %+v (err=%v), want none", ids, err)
	}

	// The same person already exists as an iMessage thread on the number.
	imID, err := st.UpsertConversation(ctx, source.IMessage, "+15550004444")
	if err != nil {
		t.Fatal(err)
	}

	// Unify identities the way the contacts page does: repoint the WhatsApp
	// conversation and its identifiers onto the iMessage contact.
	var keepContact, loseContact int64
	if err := st.DB().QueryRow(`SELECT contact_id FROM conversations WHERE id = ?`, imID).Scan(&keepContact); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT contact_id FROM conversations WHERE id = ?`, wa.ID).Scan(&loseContact); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE contact_identifiers SET contact_id = ? WHERE contact_id = ?`, keepContact, loseContact); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE conversations SET contact_id = ? WHERE id = ?`, keepContact, wa.ID); err != nil {
		t.Fatal(err)
	}

	// The WhatsApp conversation's header chips now show the iMessage handle…
	ids, err := st.ConversationIdentifiers(ctx, wa.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].Source != source.IMessage || ids[0].Identifier != "+15550004444" {
		t.Errorf("whatsapp-side identifiers = %+v, want [{imessage +15550004444}]", ids)
	}
	// …and the iMessage conversation shows the WhatsApp identity.
	ids, err = st.ConversationIdentifiers(ctx, imID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].Source != source.WhatsApp || ids[0].Identifier != "15550004444" {
		t.Errorf("imessage-side identifiers = %+v, want [{whatsapp 15550004444}]", ids)
	}
}

func TestImportArchiveNotFound(t *testing.T) {
	st := newStore(t)
	_, err := Run(context.Background(), st, Options{
		ArchiveRoot: filepath.Join(t.TempDir(), "nope"),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("err = %v, want ErrArchiveNotFound", err)
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
