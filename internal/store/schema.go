package store

// schemaVersion is the current schema revision, recorded in SQLite's
// `user_version` pragma. On Open, the migrations runner brings any older
// database forward to this version. Bump it and append a migration whenever the
// schema changes.
const schemaVersion = 3

// migrations is the ordered list of per-version migrations applied on Open.
// Each entry's index is its version (1-based; index 0 is unused).
//
// Invariant: every migration MUST be idempotent within its version transition.
// The runner wraps each entry in a transaction and only sets `user_version`
// after the transaction commits.
//
// Design notes:
//   - v1 lays down the original Signal-only schema (conversations / messages /
//     attachments / links / snapshots / ingest_state / ingest_runs / FTS5
//     virtual table + triggers).
//   - v2 introduces the unified contacts model (`contacts` and
//     `contact_identifiers`) and adds a `source` column to conversations,
//     messages, and ingest_runs so the store can hold data from Signal AND
//     iMessage (and future sources) at once. Existing rows are stamped
//     source='signal' and each Signal conversation is bootstrapped with a
//     contact and identifier; see internal/source for the canonical names.
var migrations = []string{
	0: "", // unused; versions are 1-based
	1: schemaV1,
	2: schemaV2,
	3: schemaV3,
}

// schemaV1 is the initial Signal-only schema. It is preserved verbatim so a
// fresh database walks through the same sequence of changes a long-lived one
// did, which makes reasoning about either trivial.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS conversations (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY,
    hash            TEXT    NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    ts              TEXT    NOT NULL,
    ts_unix         INTEGER NOT NULL,
    sender          TEXT    NOT NULL,
    body            TEXT    NOT NULL,
    is_system       INTEGER NOT NULL DEFAULT 0,
    seq             INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_conv_ts ON messages(conversation_id, ts_unix);
CREATE INDEX IF NOT EXISTS idx_messages_sender  ON messages(sender);
CREATE INDEX IF NOT EXISTS idx_messages_ts_unix ON messages(ts_unix);

CREATE TABLE IF NOT EXISTS attachments (
    id            INTEGER PRIMARY KEY,
    message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind          TEXT    NOT NULL,
    rel_path      TEXT    NOT NULL,
    original_name TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_id);
CREATE INDEX IF NOT EXISTS idx_attachments_kind    ON attachments(kind);

CREATE TABLE IF NOT EXISTS links (
    id         INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    url        TEXT    NOT NULL,
    domain     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_links_message ON links(message_id);
CREATE INDEX IF NOT EXISTS idx_links_domain  ON links(domain);

CREATE TABLE IF NOT EXISTS snapshots (
    id          INTEGER PRIMARY KEY,
    filename    TEXT    NOT NULL UNIQUE,
    taken_at    TEXT    NOT NULL,
    taken_unix  INTEGER NOT NULL,
    size_bytes  INTEGER NOT NULL,
    tier        TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS ingest_state (
    conversation_id  INTEGER PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    rel_path         TEXT    NOT NULL,
    mtime_unix       INTEGER NOT NULL,
    size_bytes       INTEGER NOT NULL,
    content_hash     TEXT    NOT NULL,
    message_count    INTEGER NOT NULL,
    last_ingested_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS ingest_runs (
    id                     INTEGER PRIMARY KEY,
    started_at             TEXT    NOT NULL,
    finished_at            TEXT    NOT NULL,
    duration_ms            INTEGER NOT NULL,
    conversations_scanned  INTEGER NOT NULL,
    conversations_changed  INTEGER NOT NULL,
    messages_total         INTEGER NOT NULL,
    messages_added         INTEGER NOT NULL,
    snapshots_seen         INTEGER NOT NULL,
    skipped_lines          INTEGER NOT NULL,
    errors                 INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    body,
    content='messages',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, body) VALUES (new.id, new.body);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, body) VALUES ('delete', old.id, old.body);
END;
CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, body) VALUES ('delete', old.id, old.body);
    INSERT INTO messages_fts(rowid, body) VALUES (new.id, new.body);
END;
`

// schemaV2 layers the unified-contacts model on top of v1. It is safe to run
// against any database at version 1 (the only path that can reach it): the new
// tables are CREATEd, conversations is rebuilt to swap UNIQUE(name) for
// UNIQUE(source, name), and every existing Signal conversation is mapped to a
// fresh contact and identifier so the journal / contacts page see a populated
// world from day one. See docs/adr/0003-dual-source-archive.md.
//
// The runner toggles foreign keys off around the apply (SQLite's recommended
// pattern for rebuilding a referenced table) and back on afterward.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS contacts (
    id           INTEGER PRIMARY KEY,
    display_name TEXT    NOT NULL,
    notes        TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS contact_identifiers (
    id         INTEGER PRIMARY KEY,
    contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    source     TEXT    NOT NULL,
    identifier TEXT    NOT NULL,
    UNIQUE(source, identifier)
);
CREATE INDEX IF NOT EXISTS idx_contact_identifiers_contact ON contact_identifiers(contact_id);

CREATE TABLE conversations_new (
    id         INTEGER PRIMARY KEY,
    source     TEXT    NOT NULL DEFAULT 'signal',
    name       TEXT    NOT NULL,
    contact_id INTEGER REFERENCES contacts(id) ON DELETE SET NULL,
    is_group   INTEGER NOT NULL DEFAULT 0,
    UNIQUE(source, name)
);
INSERT INTO conversations_new (id, source, name, contact_id, is_group)
    SELECT id, 'signal', name, NULL, 0 FROM conversations;
DROP TABLE conversations;
ALTER TABLE conversations_new RENAME TO conversations;

ALTER TABLE messages    ADD COLUMN source TEXT NOT NULL DEFAULT 'signal';
ALTER TABLE ingest_runs ADD COLUMN source TEXT NOT NULL DEFAULT 'signal';

-- Bootstrap one contact per existing conversation. Matching by display_name is
-- safe ONLY here: at v1 conversations.name was UNIQUE, and this migration sees
-- Signal data exclusively, so the name→contact join is unambiguous and the
-- LIMIT 1 never discards a distinct person. DO NOT copy this match-by-name
-- pattern into the iMessage importer (Slice 2.5): once two sources share a
-- display_name it would silently merge two different people. Cross-source
-- linking is a deliberate, user-confirmed action on the contacts page
-- (ADR-0003), never a name-equality heuristic.
INSERT INTO contacts (display_name)
    SELECT name FROM conversations;
UPDATE conversations
   SET contact_id = (
       SELECT c.id FROM contacts c WHERE c.display_name = conversations.name LIMIT 1
   );
INSERT INTO contact_identifiers (contact_id, source, identifier)
    SELECT contact_id, source, name FROM conversations WHERE contact_id IS NOT NULL;
`

// schemaV3 adds the vector embeddings table for semantic search.
//
// Embeddings are keyed by the STABLE message hash (messages.hash), not the
// rowid: ReplaceConversationMessages deletes and re-inserts a conversation's
// rows on every re-ingest (changing rowids but not hashes), so keying by hash
// means unchanged messages keep their embeddings and only new/changed content
// is re-embedded. For that reason there is deliberately NO foreign key to
// messages — a CASCADE would wipe embeddings on every re-ingest. Embeddings for
// truly-deleted messages are harmless orphans that `embed --prune` can reclaim.
//
// vec is a little-endian float32 blob of length dim*4; model records which
// embedding model produced it so a model change invalidates the row.
const schemaV3 = `
CREATE TABLE IF NOT EXISTS embeddings (
    message_hash TEXT    PRIMARY KEY,
    model        TEXT    NOT NULL,
    dim          INTEGER NOT NULL,
    vec          BLOB    NOT NULL
);
`
