---
title: First import
sidebar_position: 4
description: Import your archives into the local database, start the web UI, and verify your setup with msgbrowse doctor.
---

# First import

With archives on disk, `msgbrowse import` ingests them into a single local
SQLite database, `msgbrowse serve` puts a web UI on top, and
`msgbrowse doctor` verifies the whole setup.

## Run the import

`import` is the all-in-one importer: it ingests every configured archive
(Signal and/or iMessage) into one database. A source whose root is unset is
skipped; a source whose root is set but missing is an error.

```sh
msgbrowse --archive-root ~/Signal-Archive \
          --imessage-archive-root ~/iMessage-Archive \
          --data-dir ./data \
          import
```

Both archive roots (and `data_dir`) can also live in `config.yaml` or
`MSGBROWSE_*` environment variables (see the
[configuration reference](../reference/configuration.md)), in which case a
bare `msgbrowse import` does the same thing. You can also import one source at
a time with `msgbrowse signal-import` or `msgbrowse imessage-import` — the
[CLI reference](../reference/cli.md) documents every command and flag.

Imports are **incremental and idempotent** — re-run after each new export and
only changed conversations are re-scanned. To ignore the incremental state and
re-scan everything (for example after re-exporting iMessage in copy mode):

```sh
msgbrowse import --full
```

### What the import reports

Each source prints a one-line summary, for example:

```
signal:   12/348 conversations changed, 152340 messages total (410 added), 0 skipped lines in 1843ms
imessage: 3/57 conversations changed, 48211 messages total (95 added), 2 skipped lines in 622ms
media:    14 transcoded, 1032 cached, 0 source-missing, 0 failed
```

- **conversations changed / scanned** — how much of the archive actually
  needed re-ingesting.
- **messages total (added)** — corpus size and what this run contributed.
- **skipped lines** — lines the parser could not interpret (a handful is
  normal; a flood suggests a format mismatch worth an issue).
- **media** — `import` automatically runs the media transcode step, converting
  HEIC/TIFF attachments to cached JPEGs so the gallery can show them. If no
  image converter is on `PATH`, the UI falls back to placeholders; install one
  and run `msgbrowse media` later.

`import` deliberately does **not** compute embeddings — run `msgbrowse embed`
separately once an LLM endpoint is configured.

## Start the UI

```sh
msgbrowse --data-dir ./data serve
```

By default this binds `127.0.0.1:8787` and opens the UI in your default
browser. Flags:

| Flag | What it does |
| --- | --- |
| `--port` | Bind port (e.g. `8888`); keeps the configured host |
| `--host` | Bind host (e.g. `127.0.0.1` or `0.0.0.0`); keeps the configured port |
| `--listen-addr` | Full `host:port`, overriding `--host`/`--port` and config |
| `--open` | Open the browser on start (default `true`; use `--open=false` for headless) |

:::warning
The UI has no authentication and binds loopback by default. A non-loopback
bind logs a warning — only expose it behind your own access control.
:::

## Verify with `msgbrowse doctor`

`doctor` is a read-only setup diagnostic. It prints one line per check —
`✓` pass, `⚠` warn, `✗` problem — with an indented hint under anything that
did not pass, and exits non-zero only if a check fails, so it is safe in
scripts.

```sh
msgbrowse doctor
```

What it checks, in order:

1. **Data directory** — `data_dir` exists and is writable; whether a database
   exists yet (a missing one is just a warning: it is created on first
   import); the on-disk schema version versus what the binary expects; and how
   many conversations and messages are imported.
2. **Signal archive root** — `archive_root` exists and contains an `export/`
   subdirectory. It specifically catches the classic mistake of pointing
   `archive_root` *at* `export/` instead of at its parent folder.
3. **iMessage archive root** — `imessage_archive_root` exists and contains
   `*.txt` files (the flat imessage-exporter output).
4. **Attachment health** — the headline check. It samples imported image
   attachments (up to 300) and classifies each path as *ok* (resolves inside
   the archive and exists), *absolute* (points outside the archive), or
   *missing*. A majority of absolute `~/Library` paths on the iMessage source
   is reported as a failure: your export was not copy-mode, and the hint tells
   you to re-run `imessage-exporter` with `-c clone` and then
   `msgbrowse import --full`.
5. **Image converter** — whether a converter (`sips`, `magick`, `convert`, or
   `heif-convert`) is on `PATH`, and how many HEIC/TIFF attachments still lack
   a cached derivative (fix with `msgbrowse media`).
6. **Embeddings** — how many messages are not yet embedded for the configured
   embedding model (fix with `msgbrowse embed`).
7. **Exporters** — whether `sigexport` and `imessage-exporter` are on `PATH`
   (warnings only; you need them only if msgbrowse runs your exports).

Optionally, `--check-llm` adds a bare TCP reachability probe of the configured
`llm.base_url` — no data is sent, and it is the only network operation doctor
ever performs:

```sh
msgbrowse doctor --check-llm
```

The report ends with a one-line summary:

```
doctor: 2 warnings, 0 problems
```

:::tip
Warnings are informational — an unset iMessage root on a Signal-only setup, or
un-embedded messages before you have an LLM endpoint, are both fine. Only `✗`
lines (and a non-zero exit) mean something needs fixing.
:::

## Where to go next

- Start [browsing your conversations](../features/browsing.md) and
  [searching your history](../features/search.md).
- Run `msgbrowse embed` to enable semantic search, and `msgbrowse facts` for
  cited AI contact facts — see [AI features](../features/ai-features.md)
  (both need the configured LLM endpoint).
- Connect Claude or another AI assistant via the
  [MCP server](../features/mcp-server.md).
- Set up a daily `msgbrowse sync` so exports, imports, media, embeddings, and
  facts stay fresh in one command.
- If a check failed or something looks wrong, the
  [troubleshooting reference](../reference/troubleshooting.md) maps every
  doctor check to its remediation.
