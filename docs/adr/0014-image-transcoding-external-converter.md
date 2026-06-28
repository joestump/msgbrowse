# ADR-0014: Transcode HEIC/TIFF via an optional external image converter

- **Status:** Accepted
- **Date:** 2026-06-28
- **Relates to:** [ADR-0013](0013-pure-go-sqlite-driver.md) (pure-Go / cgo-free preference), [ADR-0010](0010-security-privacy-posture.md) (local-only, no network egress), [ADR-0005](0005-imessage-txt-parser.md) (iMessage source)

## Context

iMessage attachments are overwhelmingly **HEIC** (Apple's format); some archives
also carry TIFF. Browsers can't render HEIC/HEIF/TIFF in an `<img>`, so the media
gallery and transcript showed broken tiles for the majority of imported photos.

The founding brief requires asking before adding any non-Go runtime dependency
beyond the LLM endpoint and SQLite. Pure-Go HEIC decoding isn't realistically
available without cgo (libde265/libheif bindings), which would undo the cgo-free
`go install` win of [ADR-0013](0013-pure-go-sqlite-driver.md). The user approved converting images at import.

## Decision

**Transcode non-web image formats to cached JPEG derivatives using whatever
image converter is already on `PATH`, as an optional, local, best-effort step.**

1. **Detected external converter, no new bundled dep.** `internal/imageconv`
   probes `PATH` for `sips` (macOS, always present), then ImageMagick `magick` /
   `convert`, then libheif `heif-convert`. The first found is used. With **none**
   installed the pipeline is a no-op (logs once) and the UI shows a placeholder —
   so this never becomes a hard requirement.
2. **At import time, incremental + idempotent.** `msgbrowse import` runs the
   transcode after importing (and there's a standalone `msgbrowse media`). Each
   derivative is a JPEG under `<data_dir>/derived/`, named by a digest of the
   source file's absolute path, and skipped if it already exists — so re-runs are
   cheap and only new photos are converted.
3. **Serving + fallback.** The media handler serves the JPEG derivative inline
   when one exists; otherwise the gallery/transcript render a labeled placeholder
   tile (filename + download link) via the `imgRenderable` check, never a broken
   `<img>`. Web-native formats (jpg/png/gif/webp/bmp) are served as-is.
4. **Subprocess, not cgo.** Shelling out keeps the binary pure-Go and statically
   linked (ADR-0013) while still decoding HEIC. The converter is invoked with a
   fixed tool name and file-path arguments (no shell), writing to a temp file
   then renaming atomically. It is a **local** subprocess — no network egress, so
   [ADR-0010](0010-security-privacy-posture.md)'s single-egress posture is preserved.

## Consequences

- HEIC photos display whenever a converter is installed (the common Mac case via
  `sips`); elsewhere install ImageMagick/libheif, or accept placeholders.
- One-time conversion cost on first import of a large archive (tens of thousands
  of images); incremental thereafter. Bounded worker concurrency.
- Derivatives live in `data_dir` (writable), never in the read-only archive.
- New, *optional* runtime dependency — the explicit exception to the founding
  "ask first" rule, scoped to local image conversion and gracefully degraded.
- Path resolution is shared with the web layer via `internal/archivepath` so both
  the server and the transcoder apply the same traversal containment.
- Deferred: in-process thumbnailing/resizing, and converting on a schedule via
  `watch`.
