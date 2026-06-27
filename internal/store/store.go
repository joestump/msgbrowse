// Package store is sigbrowse's persistence layer: a single SQLite database
// (relational tables + an FTS5 index, with a vector index added in a later
// slice) that lives in the writable data directory, never inside the read-only
// archive.
//
// The database is opened with WAL journaling, foreign keys enforced, and a busy
// timeout so concurrent web readers coexist with the ingester's writes. All
// schema lives in schema.go and is applied idempotently on Open.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	"github.com/joestump/sigbrowse/internal/signal"
	_ "github.com/mattn/go-sqlite3" // SQLite driver (build with -tags sqlite_fts5)
)

// Store wraps the SQLite handle and exposes typed repository methods.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and applies the
// schema. The caller owns Close. path is a filesystem path, not a DSN.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?" + url.Values{
		"_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"ON"},
		"_synchronous":  {"NORMAL"},
	}.Encode()

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles one writer at a time; let database/sql keep a small pool for
	// concurrent readers while writes serialize via the busy timeout.
	db.SetMaxOpenConns(8)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for read queries added in later slices.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies the schema and records the schema version. The schema is
// written to be safe to re-run, so this is also how upgrades are staged.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

// IngestState is the per-conversation incremental bookkeeping that lets ingest
// skip unchanged chat.md files.
type IngestState struct {
	ConversationID int64
	RelPath        string
	MTimeUnix      int64
	SizeBytes      int64
	ContentHash    string
	MessageCount   int
	LastIngestedAt time.Time
}

// Snapshot describes one encrypted raw-DB backup tar under .snapshots/.
type Snapshot struct {
	Filename  string
	TakenAt   time.Time
	SizeBytes int64
	Tier      string
}

// IngestRun is a summary of a single ingest pass, surfaced on the /status page.
type IngestRun struct {
	StartedAt            time.Time
	FinishedAt           time.Time
	DurationMS           int64
	ConversationsScanned int
	ConversationsChanged int
	MessagesTotal        int
	MessagesAdded        int
	SnapshotsSeen        int
	SkippedLines         int
	Errors               int
}

// UpsertConversation returns the id of the conversation with the given name,
// inserting it if absent.
func (s *Store) UpsertConversation(ctx context.Context, name string) (int64, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations(name) VALUES(?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
		return 0, fmt.Errorf("upsert conversation %q: %w", name, err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup conversation %q: %w", name, err)
	}
	return id, nil
}

// GetIngestState returns the stored state for a conversation, or (nil, nil) if
// the conversation has never been ingested.
func (s *Store) GetIngestState(ctx context.Context, convID int64) (*IngestState, error) {
	var (
		st     IngestState
		lastAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT conversation_id, rel_path, mtime_unix, size_bytes, content_hash, message_count, last_ingested_at
		   FROM ingest_state WHERE conversation_id = ?`, convID).
		Scan(&st.ConversationID, &st.RelPath, &st.MTimeUnix, &st.SizeBytes, &st.ContentHash, &st.MessageCount, &lastAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ingest state: %w", err)
	}
	st.LastIngestedAt, _ = time.Parse(time.RFC3339, lastAt)
	return &st, nil
}

// SetIngestState upserts the incremental bookkeeping for a conversation.
func (s *Store) SetIngestState(ctx context.Context, st IngestState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ingest_state
		   (conversation_id, rel_path, mtime_unix, size_bytes, content_hash, message_count, last_ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET
		   rel_path=excluded.rel_path,
		   mtime_unix=excluded.mtime_unix,
		   size_bytes=excluded.size_bytes,
		   content_hash=excluded.content_hash,
		   message_count=excluded.message_count,
		   last_ingested_at=excluded.last_ingested_at`,
		st.ConversationID, st.RelPath, st.MTimeUnix, st.SizeBytes, st.ContentHash,
		st.MessageCount, st.LastIngestedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set ingest state: %w", err)
	}
	return nil
}

// ReplaceConversationMessages atomically replaces all messages (and their
// attachments and links) for a conversation with the supplied set. This makes
// re-ingesting a changed chat.md fully idempotent and correctly reflects edits
// and deletions, while messages.hash still uniquely keys each message. It
// returns the number of messages written.
func (s *Store) ReplaceConversationMessages(ctx context.Context, convID int64, msgs []signal.Message) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// Cascade deletes remove attachments and links for these messages.
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id = ?`, convID); err != nil {
		return 0, fmt.Errorf("clear messages: %w", err)
	}

	insMsg, err := tx.PrepareContext(ctx,
		`INSERT INTO messages(hash, conversation_id, ts, ts_unix, sender, body, is_system, seq)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insMsg.Close()
	insAtt, err := tx.PrepareContext(ctx,
		`INSERT INTO attachments(message_id, kind, rel_path, original_name) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insAtt.Close()
	insLink, err := tx.PrepareContext(ctx,
		`INSERT INTO links(message_id, url, domain) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insLink.Close()

	for i := range msgs {
		m := &msgs[i]
		res, err := insMsg.ExecContext(ctx,
			m.ID(), convID, m.TimestampRaw, m.Timestamp.Unix(), m.Sender, m.Body, boolToInt(m.IsSystem), m.Seq)
		if err != nil {
			return 0, fmt.Errorf("insert message %s: %w", m.ID(), err)
		}
		mid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		for _, a := range m.Attachments {
			if _, err := insAtt.ExecContext(ctx, mid, string(a.Kind), a.RelPath, a.OriginalName); err != nil {
				return 0, fmt.Errorf("insert attachment: %w", err)
			}
		}
		for _, l := range m.Links {
			if _, err := insLink.ExecContext(ctx, mid, l.URL, signal.Domain(l.URL)); err != nil {
				return 0, fmt.Errorf("insert link: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(msgs), nil
}

// ReplaceSnapshots replaces the snapshots inventory with the supplied set. The
// inventory is small and fully derived from the filesystem, so a full replace
// keeps it trivially consistent on every ingest.
func (s *Store) ReplaceSnapshots(ctx context.Context, snaps []Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots`); err != nil {
		return fmt.Errorf("clear snapshots: %w", err)
	}
	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO snapshots(filename, taken_at, taken_unix, size_bytes, tier) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, sn := range snaps {
		if _, err := ins.ExecContext(ctx,
			sn.Filename, sn.TakenAt.Format(signal.TimestampLayout), sn.TakenAt.Unix(), sn.SizeBytes, sn.Tier); err != nil {
			return fmt.Errorf("insert snapshot %q: %w", sn.Filename, err)
		}
	}
	return tx.Commit()
}

// RecordIngestRun stores a run summary and returns its id.
func (s *Store) RecordIngestRun(ctx context.Context, r IngestRun) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ingest_runs
		   (started_at, finished_at, duration_ms, conversations_scanned, conversations_changed,
		    messages_total, messages_added, snapshots_seen, skipped_lines, errors)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.StartedAt.UTC().Format(time.RFC3339), r.FinishedAt.UTC().Format(time.RFC3339), r.DurationMS,
		r.ConversationsScanned, r.ConversationsChanged, r.MessagesTotal, r.MessagesAdded,
		r.SnapshotsSeen, r.SkippedLines, r.Errors)
	if err != nil {
		return 0, fmt.Errorf("record ingest run: %w", err)
	}
	return res.LastInsertId()
}

// CountMessages returns the total number of messages in the database.
func (s *Store) CountMessages(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n)
	return n, err
}

// CountConversationMessages returns the number of messages in one conversation.
func (s *Store) CountConversationMessages(ctx context.Context, convID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE conversation_id = ?`, convID).Scan(&n)
	return n, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
