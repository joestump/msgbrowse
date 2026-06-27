package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
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
	dsn := "file:" + path + "?" + url.Values{
		"_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"ON"},
		"_synchronous":  {"NORMAL"},
	}.Encode()

	db, err := sql.Open("sqlite3", dsn)
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
