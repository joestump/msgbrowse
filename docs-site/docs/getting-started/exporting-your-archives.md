---
title: Exporting your archives
sidebar_position: 3
description: Produce the on-disk Signal and iMessage archives msgbrowse reads, with msgbrowse export or the upstream tools directly.
---

# Exporting your archives

msgbrowse never reads Signal's database or iMessage's `chat.db` directly. It
reads **on-disk archives** produced by two upstream exporters, and treats those
archives as strictly read-only. You can run the exporters yourself, or let
`msgbrowse export` orchestrate both in one step.

## The layout msgbrowse expects

**Signal** (`signal-export`): a root folder containing an `export/`
subdirectory with one folder per conversation:

```
Signal-Archive/
├── export/
│   └── ConversationName/
│       ├── chat.md          # the conversation, plaintext Markdown
│       └── media/           # attachments for this conversation
└── .snapshots/              # optional encrypted DB backups (listed, never opened)
```

**iMessage** (`imessage-exporter`, `-f txt`): a flat directory of
`ChatName.txt` files plus copied attachments.

## Option 1: `msgbrowse export`

`msgbrowse export` runs both upstream tools into your configured archive
roots, streaming their output to your terminal:

```sh
msgbrowse --archive-root ~/Signal-Archive \
          --imessage-archive-root ~/iMessage-Archive \
          export
```

Under the hood it runs:

- **Signal:** `sigexport <archive_root>/export` — so each chat lands at
  `<archive_root>/export/<conversation>/chat.md` plus its `media/` folder,
  exactly the layout the importer scans.
- **iMessage:** `imessage-exporter -f txt -c clone -o <imessage_archive_root>`
  — copy mode is **always** used, so attachments are bundled into the archive.

A source whose archive root is unset is simply skipped, so a Signal-only or
iMessage-only setup just works. msgbrowse stores no secrets and reads no
Keychain — the invoked tools do, with your consent.

### `export` flags

| Flag | What it does |
| --- | --- |
| `--signal-export-bin` | Path to the Signal exporter (default: `sigexport` on `PATH`; or set `signal_export_bin` in config) |
| `--imessage-exporter-bin` | Path to `imessage-exporter` (default: on `PATH`; or set `imessage_exporter_bin`) |
| `--signal-export-args` | Extra arg passed only to `sigexport` (repeatable) |
| `--imessage-exporter-args` | Extra arg passed only to `imessage-exporter` (repeatable) |
| `--skip-on-error` | Log and skip a failing or missing source instead of aborting (the run still exits non-zero) |

Trailing `-- <args>` are appended to **both** tools' command lines — use the
per-tool flags for arguments meant for one tool only:

```sh
# pass --verbose to both exporters
msgbrowse export -- --verbose

# pass an argument to sigexport only
msgbrowse export --signal-export-args=--overwrite
```

If a configured source's tool is missing from `PATH`, `export` fails with an
error naming the tool and how to install it (`pipx install signal-export` /
`brew install imessage-exporter`).

## Option 2: run the upstream exporters yourself

```sh
# Signal — writes export/<conversation>/chat.md + media/
sigexport ~/Signal-Archive/export

# iMessage — txt format, copy mode, into the archive root
imessage-exporter -f txt -c clone -o ~/iMessage-Archive
```

## iMessage: two gotchas

:::warning Full Disk Access is required
`imessage-exporter` reads `~/Library/Messages/chat.db`, which macOS protects.
The terminal (or scheduled job) running the export must have **Full Disk
Access** granted in System Settings → Privacy & Security. Without it the
export fails or comes up empty. This is a one-time manual grant.
:::

:::warning Always export in copy mode (`-c clone`)
Run `imessage-exporter` with `-c`/`--copy-method` (e.g. `-c clone`). Without
copy mode, the export records attachments as **absolute `~/Library/...` path
references** instead of copying the files into the archive — so msgbrowse can
index your messages but **no media will render**. `msgbrowse export` always
passes `-c clone` for exactly this reason, and `msgbrowse doctor` diagnoses
this exact case: if your imported iMessage attachments are mostly absolute
paths, it tells you to re-export with `-c clone` and re-run
`msgbrowse import --full`.
:::

## One-command refresh: `msgbrowse sync`

Once things work, `msgbrowse sync` chains the whole pipeline — **export →
import → media → embed → facts** — reusing each command's logic and sharing
one database handle:

```sh
msgbrowse sync
```

Stage-skipping flags: `--no-export`, `--no-media`, `--no-embed`, `--no-facts`.
`sync` also accepts the same exporter flags as `export`
(`--signal-export-bin`, `--imessage-exporter-bin`, `--signal-export-args`,
`--imessage-exporter-args`, and trailing `-- <args>`) — see the
[CLI reference](../reference/cli.md) for the full flag tables.

Failures in the hard stages (export, import, media) abort the run unless
`--skip-on-error`, which logs a warning and continues (the run still exits
non-zero). The LLM-dependent stages (embed, facts) **always** warn and
continue on failure — so a fully local run with no reachable LLM endpoint
still completes export, import, and media, and exits successfully.

## Automating the exports

The archives grow as you re-export, and imports are incremental — so a daily
scheduled export plus `msgbrowse sync` keeps everything fresh. The
[README](https://github.com/joestump/msgbrowse#setting-up-the-backup-pipeline-in-claude-cowork)
includes ready-to-paste Claude Cowork prompts that set up daily `launchd` jobs
on macOS for both sources, including snapshot retention for Signal.

## Next step

Archives on disk? Load them: [First import](first-import.md).
