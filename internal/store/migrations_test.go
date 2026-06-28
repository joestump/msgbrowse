package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
	_ "modernc.org/sqlite"
)

// TestMigrateV1ToV2BootstrapsContacts builds a database at schema version 1
// (the original Signal-only shape), runs the migrate runner, and asserts that
// every existing Signal conversation is bootstrapped with a contact and a
// contact_identifier — the core load-bearing behavior of Slice 1.5.
//
// This test deliberately reaches under Open() to construct a v1 database
// directly, because Open() always migrates to the latest version. Without that
// shortcut there is no way to exercise the v1 → v2 transition.
func TestMigrateV1ToV2BootstrapsContacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1-to-v2.sqlite")
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(NORMAL)")
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Apply v1 schema and stamp the user_version.
	if _, err := db.ExecContext(ctx, schemaV1); err != nil {
		t.Fatalf("v1 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		t.Fatalf("stamp v1: %v", err)
	}
	// Seed two Signal conversations.
	if _, err := db.ExecContext(ctx, `INSERT INTO conversations(name) VALUES('Harper'), ('MJ')`); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}

	// Run the migrate runner through a fresh Store wrapper on the same DB.
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate v1→v2: %v", err)
	}

	v, err := readUserVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}

	// Both conversations got a contact + identifier.
	var contactCount, identCount int
	if err := db.QueryRow(`SELECT count(*) FROM contacts`).Scan(&contactCount); err != nil {
		t.Fatal(err)
	}
	if contactCount != 2 {
		t.Errorf("contacts = %d, want 2", contactCount)
	}
	if err := db.QueryRow(`SELECT count(*) FROM contact_identifiers WHERE source = 'signal'`).Scan(&identCount); err != nil {
		t.Fatal(err)
	}
	if identCount != 2 {
		t.Errorf("contact_identifiers (signal) = %d, want 2", identCount)
	}

	// Every conversation row got a contact_id link.
	var unlinked int
	if err := db.QueryRow(`SELECT count(*) FROM conversations WHERE contact_id IS NULL`).Scan(&unlinked); err != nil {
		t.Fatal(err)
	}
	if unlinked != 0 {
		t.Errorf("conversations with NULL contact_id = %d, want 0", unlinked)
	}

	// Source column was added to conversations and stamped 'signal'.
	var nonSignalConv int
	if err := db.QueryRow(`SELECT count(*) FROM conversations WHERE source != 'signal'`).Scan(&nonSignalConv); err != nil {
		t.Fatal(err)
	}
	if nonSignalConv != 0 {
		t.Errorf("conversations with non-signal source = %d, want 0", nonSignalConv)
	}
}

// TestMigrateV5AddsPinnedColumn confirms schema v5 adds conversations.pinned as
// a NOT NULL column defaulting to 0 (REQ-0006-010): a fresh database has the
// column and a newly-inserted conversation is unpinned by default.
func TestMigrateV5AddsPinnedColumn(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Column exists with a 0 default.
	var dflt sql.NullString
	var notNull int
	found := false
	rows, err := st.DB().QueryContext(ctx, `PRAGMA table_info(conversations)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid    int
			name   string
			ctype  string
			nn     int
			defval sql.NullString
			pk     int
		)
		if err := rows.Scan(&cid, &name, &ctype, &nn, &defval, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "pinned" {
			found = true
			dflt = defval
			notNull = nn
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("conversations.pinned column missing after migrate")
	}
	if notNull != 1 {
		t.Errorf("pinned should be NOT NULL (notnull=%d)", notNull)
	}
	if !dflt.Valid || dflt.String != "0" {
		t.Errorf("pinned default = %q, want \"0\"", dflt.String)
	}

	// A brand-new conversation defaults to unpinned.
	id, err := st.UpsertConversation(ctx, source.Signal, "Harper")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	conv, err := st.GetConversationByID(ctx, id)
	if err != nil || conv == nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Pinned {
		t.Error("new conversation should default to unpinned")
	}
}

// TestMigrateFreshDBStampsLatest ensures a brand-new database lands directly
// on the latest schema version after Open().
func TestMigrateFreshDBStampsLatest(t *testing.T) {
	st := newTestStore(t)
	v, err := readUserVersion(context.Background(), st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Errorf("fresh DB user_version = %d, want %d", v, schemaVersion)
	}
}
