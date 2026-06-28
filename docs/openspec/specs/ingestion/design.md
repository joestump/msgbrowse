# SPEC-0001 Design: Archive Ingestion

- **Capability:** ingestion
- **Related ADRs:** [ADR-0003](../../../adr/0003-dual-source-archive.md), [ADR-0005](../../../adr/0005-imessage-txt-parser.md), [ADR-0001](../../../adr/0001-sqlite-driver-mattn-cgo.md)

## Architecture

Ingestion is split into source-specific parsers and a shared, source-agnostic
store path:

```
signal-export archive ──▶ internal/signal.Parse  ─┐
                                                  ├─▶ []signal.Message ──▶ internal/store
imessage-exporter txt ──▶ internal/imessage.Parse ┘        (Upsert + Replace + ingest_state)
       internal/ingest.Run (Signal walk + snapshots)
       internal/imessage.Run (iMessage flat walk)
```

- **`internal/signal`** (`parser.go`, `message.go`, `url.go`) parses `chat.md`
  into `signal.Message` records. It is pure: it reads an `io.Reader` and performs
  no I/O policy. `message.go` defines the canonical post-parse message shape and
  the content-hash functions (`ID`, `HashWithSource`).
- **`internal/imessage`** (`parser.go`, `import.go`) parses the imessage-exporter
  txt format into the **same** `signal.Message` shape, tagged `source="imessage"`
  at write time. Per ADR-0005, the message model after parsing is genuinely the
  same across sources, so the two parsers are sibling packages that share one
  data type rather than a generalized engine.
- **`internal/ingest`** (`ingest.go`, `snapshots.go`) is the Signal driver: it
  walks `export/<conv>/chat.md`, applies the incremental check, and inventories
  `.snapshots/`. `internal/imessage/import.go` is the iMessage driver and mirrors
  the same incremental/idempotent contract over a flat `<ChatName>.txt` layout.
- **`internal/source`** holds the canonical, never-renamed source string
  constants and `IsKnown`.
- **`internal/store`** (`store.go`, `schema.go`, `migrations.go`) owns the unified
  SQLite schema and the transactional write methods.

## Key design decisions

### Why one parser per source, sharing a data shape (ADR-0003, ADR-0005)

Each exporter has source-specific quirks — Signal uses bracketed timestamps and
Markdown markup with explicit image/link syntax; iMessage uses a flat block format
with space-padded am/pm timestamps and unmarked attachment paths. A
lowest-common-denominator parser would lose detail. Two parsers emitting the same
`signal.Message` keeps everything downstream (store, FTS5, embeddings, MCP, web UI)
written once.

### Incremental check: mtime/size fast path then content hash

`ingestConversation` / `importConversation` first compares `(mtime, size)`; if both
match, the file is skipped without reading it — a deliberate optimization over
"always hash" because real edits change at least the mtime and nearly always the
size. When metadata differs but the streamed SHA-256 content hash matches, only
`ingest_state` is refreshed (no re-parse). Only a true content change triggers a
re-parse. `--full` is the escape hatch for the pathological equal-mtime-equal-size
in-place edit. The hash is computed with a streaming `io.Copy` so large transcripts
never load fully into memory.

### Atomic replace, not diff (`ReplaceConversationMessages`)

Rather than diffing parsed messages against stored rows, a changed conversation is
replaced wholesale inside one transaction: delete all of the conversation's
messages (FK cascade removes attachments and links), then re-insert. This is the
simplest correct way to reflect edits and deletions, and it is safe because every
message keys on a stable hash, so embeddings keyed by that hash (SPEC-0002) survive
the rowid churn.

### Source-namespaced hash, not a composite key (ADR-0005)

With two sources, two conversations called "MJ" could produce identical
`Message.ID` values. Rather than migrate `messages` to a composite key, the source
is folded into the conversation component of the hash via `HashWithSource`. This is
a one-line, self-healing change: re-importing re-keys rows as a no-op replace, and
the only fallout is orphaned embeddings under the old hash, which `embed --prune`
reclaims.

### Contacts bootstrap, never auto-merge (ADR-0003)

`UpsertConversation` is a transactional find-or-create across `conversations`,
`contacts`, and `contact_identifiers`. First sight of a `(source, identifier)`
auto-creates a contact whose display name equals the conversation name and links
it. Cross-source identity reconciliation (e.g. merging `signal:MJ` with
`imessage:+1555…`) is a deliberate, user-confirmed action on the contacts page —
never an import-time heuristic — because a wrong merge corrupts per-person history
irrecoverably. The v2 migration's match-by-display-name bootstrap is safe ONLY
because at v1 the data was Signal-only and names were unique; that pattern is
explicitly NOT reused by the iMessage importer.

### Schema and migrations (ADR-0001, ADR-0003)

The store uses the pure-Go `modernc.org/sqlite` driver (FTS5 built in, no cgo or
build tag — ADR-0013, superseding the cgo driver of ADR-0001), opened in WAL mode
with foreign keys on, `busy_timeout`, and IMMEDIATE-mode
write transactions (so `busy_timeout` applies to the initial lock acquisition
rather than a lock upgrade). Schema is applied by a versioned migration runner
(`user_version` pragma): v1 lays down the Signal-only schema, v2 adds the unified
contacts model and `source` columns (rebuilding `conversations` to swap
`UNIQUE(name)` for `UNIQUE(source, name)` with foreign keys toggled off and
verified off around the rebuild), v3 adds the embeddings table. v6 adds the
`reactions` table (issue #50): like embeddings and contact_facts it keys on the
stable per-source message hash (no FK to `messages`, so re-ingest doesn't cascade
them away) and carries a `conversation_id` FK purely so
`ReplaceConversationMessages` can clear-and-reinsert a conversation's reactions in
the same idempotent transaction as its messages. Each migration runs in its own
transaction with a `foreign_key_check` belt-and-suspenders before committing.

### Snapshots are a passive inventory

`scanSnapshots` lists `.snapshots/db-*.tar`, derives a GFS retention tier from
file age, and replaces the inventory each pass. msgbrowse never creates, prunes,
opens, or decrypts snapshots — they are SQLCipher-encrypted disaster-recovery
backups owned by the upstream backup job. A file vanishing mid-scan (a race with
that job) is treated as benign.

## Trade-offs

- The mtime/size fast path can miss an equal-length in-place edit until `--full`;
  accepted as the documented contract.
- Wholesale replace re-inserts unchanged rows on any content change; at
  personal-archive scale this is cheap and far simpler than a correct diff.
- Heuristic iMessage attachment detection can miss an attachment (it stays
  searchable as body text) or rarely 404 a false positive in the gallery — the
  safe failure direction per ADR-0005.
