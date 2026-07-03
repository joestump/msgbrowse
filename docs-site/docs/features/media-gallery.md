---
title: Media gallery
sidebar_label: Media gallery
sidebar_position: 3
---

# Media gallery

The gallery collects every image, file, and link across your entire archive
into one browsable page — served locally from your read-only archive, never
uploaded anywhere.

## Tabs

The gallery has three tabs, each with a live count badge, and a shared filter
row (conversation, source, and date range):

- **Images** — a responsive square grid of thumbnails. Clicking a thumbnail
  opens a **lightbox** (pure CSS `:target`, no JavaScript, so the strict
  Content-Security-Policy stays intact) with a caption linking back to the
  message in its conversation.
- **Files** — non-image attachments as cards with the original filename,
  content type, size, timestamp, source pill, and a link to the message they
  came from.
- **Links** — every URL extracted from message bodies, deduplicated and
  **grouped by domain**, with a repeat count for links shared more than once
  and a link back to the sending message.

## HEIC and TIFF transcoding

Browsers can't render Apple HEIC/HEIF or TIFF images, which are common in
iMessage (and some Signal) archives. msgbrowse transcodes them to cached JPEG
derivatives via the `imageconv` pipeline:

```sh
msgbrowse --data-dir ./data media
```

- `import` runs this step automatically; the standalone `media` command lets
  you re-run it (for example after installing a converter).
- It shells out to the first image converter found on `PATH`, in order:
  `sips` (macOS, always present), `magick` (ImageMagick 7), `convert`
  (ImageMagick 6), or `heif-convert` (libheif). This is an optional, local,
  non-network dependency.
- Derivatives are cached under `<data_dir>/derived`, keyed by source path, so
  the run is incremental and idempotent. `--force` re-converts everything;
  `--concurrency` controls parallelism (default 6).
- The archive itself is never modified — derivatives live in your writable
  data directory only.

**Placeholder fallback:** with no converter installed, transcoding is a no-op
(not an error). Un-transcoded images render as a striped "no preview" tile in
the gallery — and a labeled chip in the transcript — that links to the
original file for download. Install a converter (e.g. ImageMagick or libheif)
and run `msgbrowse media` to fill the previews in.

:::warning iMessage attachments need copy mode + Full Disk Access
`imessage-exporter` must run with `-c clone` (or another copy mode) **and**
Full Disk Access, or your export's attachments are absolute-path references
into `~/Library/Messages` that msgbrowse cannot serve — every image shows as
missing. `msgbrowse doctor` diagnoses this exact case: its headline check
samples imported attachment paths and reports how many resolve inside the
archive versus how many are absolute references. If it flags absolute paths,
re-export with `-c clone` and re-import.
:::

## Configuration

| Key | What it shapes |
| --- | --- |
| `data_dir` | Where transcoded JPEG derivatives are cached (`<data_dir>/derived`). |
| `archive_root` / `imessage_archive_root` | The read-only roots media files are served from. |
