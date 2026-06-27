# msgbrowse

> Self-hosted, local-only browser, search engine, and **AI-editorialized
> journal** over your personal message archives — Signal (today) and Apple
> iMessage (Slice 2.5) — from the two upstream CLI exporters
> [`signal-export`](https://github.com/carderne/signal-export) and
> [`imessage-exporter`](https://github.com/ReagentX/imessage-exporter).
> Think *Backrest-for-Restic*, but for your chat history.

msgbrowse renders a clean local UI over on-disk Markdown exports, adds fast
keyword + semantic search, and exposes an **MCP server** so Claude can answer
natural-language questions over your message history. The headline feature is
the **editorialized journal** — Daylio-style daily cards (Highlights / People /
Themes / Mood / Standout media / Notable links) the LLM writes for you across
*every* source, with received media as first-class content.

It runs entirely on your machine via Docker. **Nothing leaves the box** except
calls to the one OpenAI-compatible LLM endpoint you configure (default: a
local LiteLLM proxy). See [`SECURITY.md`](SECURITY.md) for the egress and
data-handling model.

> **Status:** under active construction, built in vertical slices. See
> [`ARCHITECTURE.md`](ARCHITECTURE.md) for the layering and the per-slice docs
> as they land. Today: Slices 1–2 (parser + SQLite + ingest + web UI +
> transcript) and 1.5 (unified contacts schema) are merged.

## Sources

| Source     | Upstream exporter                                                                     | Subcommand        | Status                                  |
| ---------- | -------------------------------------------------------------------------------------- | ----------------- | --------------------------------------- |
| Signal     | [carderne/signal-export](https://github.com/carderne/signal-export)                    | `signal-import`   | ✅ wired                                |
| iMessage   | [ReagentX/imessage-exporter](https://github.com/ReagentX/imessage-exporter)            | `imessage-import` | Slice 2.5                               |

Each source's rows are tagged in the unified store (`source='signal'`,
`source='imessage'`); a per-source contact and identifier are auto-created and
the user merges duplicates on the **Contacts** page (Slice 4.5) — never silent
auto-merge. See [ADR-0003](docs/adr/0003-dual-source-archive.md).

## Features (target)

1. **Browse backups** — inventory the encrypted `.snapshots/*.tar` raw-DB backups
   with GFS retention tiers and total footprint (listed, never decrypted).
2. **Browse by person across sources** — sidebar of every contact; transcript
   view with inline media interleaved across Signal and iMessage.
3. **Search** — SQLite FTS5 keyword search with filters (source, contact,
   sender, date, has-media, has-link) and live HTMX results.
4. **Semantic search + RAG over MCP** — a vector index plus an MCP server with
   citation-faithful, contact-keyed (source-blind) retrieval tools.
5. **Browse by media type** — image gallery, file list, deduplicated links.
6. **Editorialized journal** — Daylio-style daily cards above the raw
   transcript; images get vision-captioned, audio gets transcribed.
7. **Contacts page** — manual merging of cross-source identities with macOS
   Contacts.app vCard suggestions.

## Engineering principles

- **Go only** for all application code (Go 1.23+), standard project layout.
- **HTMX** + server-rendered `html/template`; no SPA, no npm build step.
- **Cobra + Viper** for the CLI and configuration (file + env + flags).
- **Tests and docs are mandatory.** See `make check`.
- **Security by default** — loopback-only, read-only archives, least privilege,
  no silent identity merging, no `.snapshots/*.tar` decryption.

## Quickstart

```sh
# Build and test locally (requires Go 1.23+; CGO for the SQLite/FTS5 driver).
make build
make test

# Import a signal-export archive into the local store.
./bin/msgbrowse --archive-root "$HOME/Managed Files/Signal-Archive" signal-import

# Serve the UI on loopback.
./bin/msgbrowse --data-dir ./data serve
# → http://127.0.0.1:8787
```

Docker compose (LiteLLM + msgbrowse), MCP client snippet, the full
configuration reference, and the **Cowork setup prompts** for both
`signal-export` and `imessage-exporter` are documented as Slice 7 lands.

## License

[MIT](LICENSE) © Joe Stump
