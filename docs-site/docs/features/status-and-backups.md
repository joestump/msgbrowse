---
title: Status & backups
sidebar_label: Status & backups
sidebar_position: 4
---

# Status & backups

The **Status & backups** page (`/status`) answers two questions at a glance:
*is my archive fresh?* and *are my disaster-recovery backups accumulating as
expected?* Like everything else in msgbrowse it is read-only and local — it
reports on your archive without touching it.

## Archive freshness

A stat strip shows the total conversation count, total message count, and the
timestamp of the newest message in the database. If the newest message is
older than you expect, your export or import pipeline has probably stalled —
run `msgbrowse sync` (or check your scheduled exporter job).

## Ingest stats

The **Last ingest** card summarizes the most recent import run:

- when it finished and how long it took,
- conversations changed vs. scanned,
- total messages and how many were added,
- skipped lines and errors.

Imports are incremental and idempotent, so a healthy steady-state run shows a
small "added" count and zero errors. A growing skipped-line count is worth a
look — it usually means the exporter's output format changed.

## Encrypted snapshot inventory

If your export pipeline persists raw database snapshots under the archive's
`.snapshots/` directory (the recommended Signal backup setup writes
SQLCipher-encrypted `db-YYYYMMDD-HHMMSS.tar` files there), the status page
inventories them: filename, when each was taken, size, its GFS retention tier
(daily / monthly / quarterly / yearly), and the total on-disk footprint.

:::warning Listed, never opened
The `.snapshots/*.tar` files are SQLCipher-encrypted disaster-recovery
backups. msgbrowse inventories them by **filename and size only** — it never
opens, decrypts, or reads their contents, and never touches the macOS
Keychain. That guarantee is part of the product's threat model (see
[SECURITY.md](https://github.com/joestump/msgbrowse/blob/main/SECURITY.md)).
:::

## Configuration

| Key | What it shapes |
| --- | --- |
| `archive_root` | Where the `.snapshots/` directory is discovered. |
| `data_dir` | Where the SQLite database with ingest-run history lives. |
