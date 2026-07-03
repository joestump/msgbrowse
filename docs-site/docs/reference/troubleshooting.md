---
sidebar_position: 4
title: Troubleshooting
---

# Troubleshooting

Start every diagnosis with the built-in doctor. It is read-only, makes no network calls (except an opt-in TCP probe behind `--check-llm`), and exits non-zero only when it finds a real problem:

```bash
msgbrowse doctor
msgbrowse doctor --check-llm
```

Each check prints `✓` (pass), `⚠` (warning), or `✗` (problem) with a remediation hint. The sections below expand on every check, then cover a few symptoms doctor can't see.

## Doctor checks and remediations

### Data directory and database

| Report | Meaning | Fix |
| --- | --- | --- |
| `data_dir is not set` / not writable / not a directory | msgbrowse has nowhere to put its database and caches. | Point `data_dir` (config), `--data-dir`, or `MSGBROWSE_DATA_DIR` at a writable directory outside the archive. |
| `data_dir ... does not exist yet` | Normal on a fresh install — the directory is created on first import. | Run `msgbrowse import` once your archive roots are configured. |
| `no database yet (no import has run)` | The data dir exists but holds no `msgbrowse.sqlite`. | Run `msgbrowse import`. |
| `database schema version N, binary expects M` | You upgraded the binary; the on-disk schema is behind. Doctor never migrates (it opens the database read-only). | Run any write-path command — `msgbrowse import` is the natural choice — and the schema migrates forward automatically. |
| `0 conversations, 0 messages` | The database exists but nothing has been imported. | Check your archive roots, then run `msgbrowse import`. |

### Archive roots

| Report | Meaning | Fix |
| --- | --- | --- |
| `archive_root ... points AT export/ (or its contents)` | The classic Signal mistake: `archive_root` must be the folder that **contains** `export/`, not `export/` itself. | Set `archive_root` to the parent folder — e.g. `.../Signal-Archive`, not `.../Signal-Archive/export`. |
| `archive_root ... has no export/ subdirectory` | The path exists but isn't a signal-export archive. | Point it at the signal-export output (which contains `export/` of per-conversation directories). |
| `imessage_archive_root ... has no *.txt files` | The path isn't imessage-exporter txt output. | Re-run `imessage-exporter -f txt -c clone -o <dir>` and point `imessage_archive_root` at that directory of `<ChatName>.txt` files. |
| `archive_root (Signal) is not set` / `imessage_archive_root is not set` | Warnings, not errors — a Signal-only or iMessage-only setup is fine. | Ignore if intentional; otherwise set the missing root. |

### Attachment health — the headline check

Doctor samples imported image attachments and classifies how each stored path resolves on disk:

| Report | Meaning | Fix |
| --- | --- | --- |
| `N iMessage attachments use absolute ~/Library paths` (✗ or ⚠) | Your imessage-exporter run was **not copy mode**: it wrote references to the originals under `~/Library` instead of copying files into the archive, so no media can render. | Re-export with a copy method — `msgbrowse export` always passes `-c clone` for you — then run `msgbrowse import --full`. |
| `N of M sampled attachments ... file is missing` | Paths resolve inside the archive but the files are gone; the archive may be incomplete or moved. | Restore or re-export the archive, keeping media alongside the transcripts. |

:::warning Full Disk Access + copy mode
Two things must be true for iMessage media to work: `imessage-exporter` needs **Full Disk Access** (System Settings → Privacy & Security) to read `~/Library/Messages/chat.db`, and it must run with a copy method (`-c clone` or `-c copy`) so attachments are bundled into the archive. `msgbrowse export` enforces the copy method; the Full Disk Access grant is a one-time manual step on your Mac.
:::

### Image converter

| Report | Meaning | Fix |
| --- | --- | --- |
| `no image converter found (sips / magick / convert / heif-convert)` | Apple HEIC/HEIF and TIFF attachments can't be transcoded to JPEG, so the gallery shows placeholders for them. | Install one converter — `sips` ships with macOS; elsewhere install ImageMagick (`magick`/`convert`) or libheif (`heif-convert`) — then run `msgbrowse media`. |
| `N convertible image(s) lack a cached derivative` | HEIC/TIFF files exist that haven't been transcoded yet. | Run `msgbrowse media`. |

### Embeddings

| Report | Meaning | Fix |
| --- | --- | --- |
| `N message(s) not embedded for model "..."` | Semantic search has no vectors for those messages. | Run `msgbrowse embed` (needs the configured LLM endpoint reachable). |

### Exporters

| Report | Meaning | Fix |
| --- | --- | --- |
| `exporter "sigexport" not found on PATH` | Only matters if you want `msgbrowse export`/`sync` to run Signal exports. | `pipx install signal-export` — note the pip package is `signal-export` but the console command is `sigexport`. Or pin a binary with `--signal-export-bin` / the `signal_export_bin` config key. |
| `exporter "imessage-exporter" not found on PATH` | Only matters if you want msgbrowse to run iMessage exports. | `brew install imessage-exporter`, or pin with `--imessage-exporter-bin` / `imessage_exporter_bin`. |

### LLM endpoint (`--check-llm` only)

| Report | Meaning | Fix |
| --- | --- | --- |
| `llm endpoint host:port not reachable` | The single configured egress (`llm.base_url`) refused a TCP connect. `embed`, `facts`, and journal digests need it; browsing and keyword search do not. | Start your local LiteLLM/Ollama stack (the default expects `http://127.0.0.1:4000/v1`), or fix `llm.base_url` / `MSGBROWSE_LLM_BASE_URL`. The probe sends no data — it opens and closes a connection. |

## Symptoms doctor can't see

### Images are broken in the transcript or gallery

Work down this list:

1. **Run `msgbrowse doctor`.** Its attachment check catches the most common cause outright: a non-copy-mode iMessage export storing absolute `~/Library` paths. Remedy: re-export with `-c clone` (what `msgbrowse export` does), then:

   ```bash
   msgbrowse import --full
   ```

2. **HEIC/TIFF placeholders.** Browsers can't render Apple's HEIC or TIFF. Install a converter (`sips`, ImageMagick, or libheif) and run:

   ```bash
   msgbrowse media
   ```

3. **Archive moved after import.** Attachment paths are resolved against the archive roots at serve time. If you relocated the archive, update `archive_root` / `imessage_archive_root` to the new location.

### Reactions show as text in message bodies

Older versions imported reaction lines (Signal's `(- Name: emoji -)` trailers and iMessage tapbacks) as message text or as standalone messages. Current versions parse them into reaction badges on the message they belong to — but only for conversations that are re-scanned. Incremental import skips unchanged conversations, so after upgrading, force a full re-scan:

```bash
msgbrowse import --full
```

:::tip
`--full` re-parses every conversation from the archive; it is safe to run anytime and never touches the archive itself. Reach for it after any upgrade that changes how messages are parsed.
:::

### The UI looks unstyled (missing CSS) or the browser console shows CSP errors

msgbrowse serves a strict Content Security Policy and loads everything same-origin — the stylesheet (`app.css`), htmx, and icons are compiled in via `go:embed`. Release binaries always contain a current stylesheet. If you built from source and the UI renders unstyled after template changes, the committed `app.css` is stale; rebuild it:

```bash
make css        # fetches the Tailwind standalone CLI into .tools/ (no npm) and rebuilds
make build
```

If `make css` produces odd output from a previously cached toolchain, clear the cache first:

```bash
rm -rf .tools && make css
```

CSP violation reports in the browser console usually mean a browser extension is injecting scripts or styles — msgbrowse itself ships no inline styles or scripts, so its own pages never trip the policy.

### `export/` not found or "archive_root must be the folder that CONTAINS export/"

The importer error and the doctor check agree: `archive_root` is the *parent* of `export/`. In Docker, point `MSGBROWSE_ARCHIVE_HOST` in `.env` at that parent folder.

### Nothing to import / nothing to export

Both `import` and `export` need at least one archive root configured. Set `archive_root` and/or `imessage_archive_root` via flags, config file, or `MSGBROWSE_ARCHIVE_ROOT` / `MSGBROWSE_IMESSAGE_ARCHIVE_ROOT`.
