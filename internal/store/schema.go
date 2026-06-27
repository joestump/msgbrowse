package store

// schemaVersion is the current schema revision, stored in SQLite's user_version
// pragma. Bump it and add a migration step whenever schema changes.
const schemaVersion = 1

// schemaSQL is the full schema applied to a fresh database. It is intentionally
// idempotent (IF NOT EXISTS) so it can run on every Open.
//
// Design notes:
//   - messages.id is an internal INTEGER rowid so the FTS5 external-content table
//     can reference it cheaply; messages.hash is the stable public identifier
//     (signal.Message.ID), unique for idempotent ingestion.
//   - attachments and links cascade-delete with their message, which lets the
//     ingester replace a changed conversation's messages atomically without
//     leaving orphans (requires PRAGMA foreign_keys=ON, set at Open).
//   - messages_fts is kept in sync by triggers, so all keyword search sees every
//     write regardless of code path.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS conversations (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY,
    hash            TEXT    NOT NULL UNIQUE,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    ts              TEXT    NOT NULL,   -- "YYYY-MM-DD HH:MM:SS", lexically sortable
    ts_unix         INTEGER NOT NULL,   -- parsed timestamp for range queries/ordering
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
    kind          TEXT    NOT NULL,   -- "image" | "file"
    rel_path      TEXT    NOT NULL,   -- path as written, relative to the conversation folder
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
    taken_at    TEXT    NOT NULL,   -- "YYYY-MM-DD HH:MM:SS" parsed from the filename
    taken_unix  INTEGER NOT NULL,
    size_bytes  INTEGER NOT NULL,
    tier        TEXT    NOT NULL    -- GFS tier: daily|monthly|quarterly|yearly
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
