# ADR-0005: iMessage source via the imessage-exporter txt format

- **Status:** Accepted
- **Date:** 2026-06-27
- **Builds on:** [ADR-0003](0003-dual-source-archive.md) (dual-source unified schema)

## Context

ADR-0003 made msgbrowse multi-source. This ADR records how the second source —
Apple iMessage — is ingested: from the on-disk output of
[`imessage-exporter`](https://github.com/ReagentX/imessage-exporter) run with
`-f txt`, parsed into the shared `signal.Message` model and tagged
`source="imessage"`.

The txt format is not formally specified; it was reverse-engineered from the
4.2.0 source (the templates in `src/exporters/txt/`). It differs from Signal's
`chat.md` in several ways that drove the design:

- **Layout is flat**: one `<ChatName>.txt` per conversation in the export root,
  plus an `attachments/` directory — not Signal's `export/<conv>/chat.md` +
  `media/` nesting.
- **Block shape**: `timestamp line` → `sender line` (`Me` / contact / handle) →
  body lines → bare attachment **path** lines → optional `Tapbacks:` / indented
  quoted replies / status notices → blank line between messages.
- **Timestamp**: `%b %d, %Y %l:%M:%S %p` with a space-padded hour (e.g.
  `May 20, 2020  9:10:11 AM`), optionally trailed by a read receipt.
- **Attachments have no marker** — they render as bare filesystem path lines,
  indistinguishable from body text except by shape.

## Decision

1. **Target imessage-exporter 4.2.0 txt.** Pin and document the supported
   version. A new `internal/imessage` package parses the format; a new
   `imessage-import` subcommand drives it with its own `imessage_archive_root`.
2. **Reuse everything downstream.** The parser emits `signal.Message`; import
   uses the same incremental/idempotent store path as Signal
   (`UpsertConversation`/`ReplaceConversationMessages`/`ingest_state`). Only the
   parser and the directory walk are source-specific.
3. **Namespace the message hash by source** (`Message.HashWithSource`): with two
   sources, conversations that share a display name (signal:"MJ", imessage:"MJ")
   could otherwise collide on the globally-unique `messages.hash`. The store now
   keys messages by `hash(source, conversation, ts, sender, body, seq)`.
4. **Attachments are detected heuristically** — a spaceless path line ending in
   a known media/document extension becomes an attachment (image vs file by
   extension); everything else is body text. Tapbacks, indented quoted replies,
   and status notices ("This message responded…", "Attachment missing!") are
   skipped. Bare http(s) URLs in body become links (shared `signal.ExtractLinks`).
5. **Media serving is source-aware.** The web media handler resolves an
   attachment under the right archive for the conversation's source — Signal:
   `<archive>/export/<conv>/<rel>`; iMessage: `<imessage_archive>/<rel>` — both
   through the same path-traversal containment check.

## Why these choices

- **txt over html/json**: txt is the smallest, most stable surface to parse and
  matches the brief's "Markdown exporter" framing; html would couple us to
  markup, and json isn't this tool's native output.
- **Heuristic attachments**: the format genuinely lacks an attachment marker, so
  perfect classification from txt alone is impossible. The spaceless-path-with-
  known-extension heuristic captures the common case (copy-mode paths like
  `attachments/AB/CD/IMG.HEIC`) and treats ambiguous prose as body — the safe
  failure direction (a missed attachment is still searchable text; a false
  attachment would merely 404 in the gallery).
- **Hash namespacing over schema change**: folding source into the hash is a
  one-line, backward-self-healing change (re-import replaces rows); it avoids a
  composite-key migration on `messages`.

## Consequences

- iMessage conversations, messages, links, and (best-effort) attachments are
  browsable and searchable alongside Signal, sharing the FTS5 index, embeddings,
  MCP tools, and (later) the journal — no per-source code in those layers.
- **The format is version-sensitive and unvalidated against a real export.** The
  parser was built from 4.2.0 source + a synthetic fixture; the attachment-path
  heuristic and exact dir layout should be confirmed against real output, and
  the parser adjusted if a different version is used. This is called out in the
  README and the `imessage-import` help.
- Re-importing existing Signal data after the hash change re-keys its messages
  (a no-op replace); embeddings keyed by the old hash become orphans reclaimed
  by `embed --prune`.

## Alternatives considered

- **Generalize the Signal ingest into a shared engine up front.** Deferred: the
  two importers share the incremental/store logic but differ in discovery and
  parsing; a premature abstraction would be shapeless. Extract later if a third
  source arrives.
- **Parse imessage-exporter's html or read chat.db directly.** Rejected: html is
  markup-coupled; chat.db is SQLCipher/format-churny and out of scope (msgbrowse
  reads exporter output, it is not itself an exporter).
