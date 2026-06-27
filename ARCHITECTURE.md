# Architecture

msgbrowse is a single Go binary that imports on-disk message-archive exports into
one SQLite database and serves three faces over it: an HTMX web UI, an MCP
server, and a journal generator. It is local-first and read-only with respect to
the archive.

## Layering

```
cmd/msgbrowse            thin main(): delegates to internal/cli
└── internal/cli         Cobra commands + Viper config wiring
    ├── internal/config  config model (defaults < file < MSGBROWSE_* env < flags)
    ├── internal/signal  signal-export chat.md parser → []signal.Message
    ├── internal/source  canonical source names (signal, imessage)
    ├── internal/ingest  scan archive, incremental idempotent import, snapshots
    ├── internal/store   SQLite: schema/migrations, relational + FTS5 + vectors
    ├── internal/llm     OpenAI-compatible client (the only network egress)
    ├── internal/embed   batch embedding orchestration
    ├── internal/mcp     Model Context Protocol server (tools over the store)
    └── internal/web     net/http + html/template + HTMX UI
```

Dependencies point inward toward `store` and `signal`; `mcp` and `web` are sibling
presentation layers that share the same `store` methods, so keyword/semantic/media
behavior cannot drift between the model-facing and human-facing surfaces.

## Data model (SQLite, one file in `data_dir`)

- `conversations(id, source, name, contact_id, is_group)` — `UNIQUE(source, name)`.
- `messages(id, hash, conversation_id, source, ts, ts_unix, sender, body, is_system, seq)`
  — `hash` is the stable content key for idempotent re-import; `id` is the FTS/cursor rowid.
- `attachments`, `links` — cascade-delete with their message.
- `contacts`, `contact_identifiers(contact_id, source, identifier)` — the unified
  identity layer; one canonical person spans Signal + iMessage handles
  (reconciled manually; see ADR-0003).
- `embeddings(message_hash, model, dim, vec)` — `PRIMARY KEY (message_hash, model)`,
  no FK (keyed by stable hash so re-import doesn't wipe vectors; multiple models
  coexist).
- `snapshots`, `ingest_state`, `ingest_runs` — backup inventory + incremental
  bookkeeping + per-run summaries.
- `messages_fts` — FTS5 external-content table kept in sync by triggers.

Schema changes go through the versioned migration runner (`internal/store/migrations.go`),
which applies each version in its own transaction and records `PRAGMA user_version`.

## Import pipeline

`signal-import` walks `export/<conv>/chat.md`, skipping unchanged files via
`(mtime, size)` then content hash recorded in `ingest_state` (`--full` forces a
rescan). Each changed conversation is parsed (streaming, malformed lines logged
and skipped — never fatal) and its messages atomically replaced in one
transaction. Re-import is idempotent; messages are keyed by a content hash with a
sequence disambiguator for byte-identical lines. The `.snapshots/*.tar`
inventory is refreshed by filename/size only.

## Search

- **Keyword**: SQLite FTS5 (`bm25` ranking), filterable by conversation, source,
  sender, date, has-attachment/has-link. Snippets are highlighted safely (the
  store emits control-char markers; the web layer escapes then swaps them for
  `<mark>`).
- **Semantic**: per-message embeddings via the LLM, searched with a brute-force
  cosine scan over the filtered candidate set (ADR-0002). The MCP
  `search_messages` tool fuses keyword + semantic with reciprocal-rank fusion and
  degrades to keyword-only when no LLM/embeddings are available.

## LLM access

One `llm.Client` interface (`Embed`, `Chat`, `Transcribe`, `Vision`) backed by an
OpenAI-compatible HTTP client, pointed by default at a local LiteLLM proxy. This
package is the sole network egress. `Transcribe`/`Vision` exist for the
media-first journal (Slice 6).

## Web UI

`net/http` with Go 1.22 pattern routing, `html/template` (auto-escaping all
untrusted message content), HTMX for partials (live search, infinite scroll). No
SPA, no Node; vendored htmx pinned by SHA.

Styling is **Tailwind CSS + daisyUI** (drawer/navbar layout, `chat` bubbles for
transcripts, `card`/`menu`/`tabs`/`stat` components) with a dim (dark) / winter
(light) **theme toggle** (`internal/web/static/theme.js`, self-hosted, persists
to `localStorage`). Icons are vendored **Hero Icons** inline SVG. The stylesheet
is built by the Tailwind **standalone CLI + daisyUI** at dev time (`make css`,
no Node) and the resulting `app.css` is committed and `go:embed`-served, so the
runtime and Docker image need no toolchain and stay CDN-free.

A strict `Content-Security-Policy` (`default-src 'none'`, `script-src 'self'`,
`style-src 'self'`, `img-src 'self' data:`) plus `nosniff`, `no-referrer`, and
frame denial harden every response; media is served with correct
`Content-Type`/`Content-Disposition` and path-traversal containment. Everything
the UI loads — CSS, htmx, the theme script, icons — is same-origin.

## Key decisions (ADRs)

- [ADR-0001](docs/adr/0001-sqlite-driver-mattn-cgo.md) — SQLite driver: mattn + cgo + `sqlite_fts5`.
- [ADR-0002](docs/adr/0002-vector-backend.md) — vector backend: brute-force default, sqlite-vec optional.
- [ADR-0003](docs/adr/0003-dual-source-archive.md) — dual-source unified schema + manual contact reconciliation.
- [ADR-0004](docs/adr/0004-mcp-sdk-and-rag.md) — official MCP SDK + citation-faithful hybrid RAG.

## Containerization

Multi-stage `Dockerfile`: cgo build on `golang:1.25-bookworm`, runtime on
distroless (`nonroot`, glibc, no shell). `docker-compose.yml` wires msgbrowse to a
LiteLLM proxy (optional Ollama behind it), bind-mounts the archive read-only,
keeps app data in a named volume, publishes the UI to host loopback only, and
hardens the app container (read-only rootfs, dropped capabilities, no privilege
escalation).
