# SPEC-0015: Telegram source

- **Status:** Accepted
- **Date:** 2026-07-06
- **Capability:** telegram-source
- **Source packages:** `internal/telegram` (new), `internal/source`, `internal/store` (allowed-source migration), `internal/archivepath`, `internal/ingest`, `internal/setup` (detection + guidance), `internal/onboard`/`internal/onboardsvc` (adopt/refresh), `internal/cli` (`import.go`, `doctor.go`, `sync.go`), `internal/config`, `internal/web` (Providers card, source styling), `internal/devsync` (folder provisioning), `docs-site`
- **Related ADRs:** [ADR-0022 (Telegram via native export)](../../../adr/0022-telegram-source-native-export.md), [ADR-0016 (WhatsApp source)](../../../adr/0016-whatsapp-source-exporter.md), [ADR-0003 (dual-source archive)](../../../adr/0003-dual-source-archive.md), [ADR-0010 (security & privacy posture)](../../../adr/0010-security-privacy-posture.md)
- **Related specs:** [SPEC-0013 (desktop guided setup)](../desktop-onboarding/spec.md), [SPEC-0001 (archive ingestion)](../ingestion/spec.md), [SPEC-0014 (Syncthing device sync)](../device-sync-syncthing/spec.md)

## Overview

Telegram becomes the fourth message source. Per ADR-0022, acquisition uses
Telegram Desktop's first-party export (Settings → Advanced → Export Telegram
data, JSON format + media) — msgbrowse never holds Telegram credentials or
bundles an MTProto client. msgbrowse detects Telegram Desktop, guides the
export, adopts the export folder as the managed `telegram` archive root, and
imports it through the existing incremental pipeline.

## Requirements

### Requirement: Source registration

The system SHALL register `telegram` as a first-class source: a
`source.Telegram` constant with the persisted literal `telegram`, membership in
`source.All`, a human label `Telegram`, and acceptance by the store's
allowed-source validation. The literal MUST never be renamed once persisted.

#### Scenario: Unified store accepts the source

- **WHEN** a conversation, message, contact identifier, or ingest run is
  written with `source = "telegram"`
- **THEN** the store accepts it, and every source-enumerating surface (search
  filters, gallery source filter, Providers, doctor, MCP tools) includes
  Telegram alongside the existing three sources.

### Requirement: Export-folder parsing

The system SHALL parse Telegram Desktop's JSON export from the archive root:
`result.json` at the root, containing `chats.list[]`, each with `messages[]`.
Parsing MUST be streaming (token-level decode), because real exports reach
multiple gigabytes; the parser MUST NOT load the whole file into memory.
Message `text` values that are entity arrays MUST be flattened to their plain
concatenated text for storage and search. Messages with `type: "service"`
SHALL be stored as system messages. Timestamps SHALL prefer `date_unixtime`
and fall back to parsing `date`. Unknown JSON fields MUST be ignored;
malformed chats or messages MUST be logged and skipped, never fatal.

#### Scenario: Entity-array text is flattened

- **WHEN** a message's `text` is `["see ", {"type": "link", "text": "https://example.com"}]`
- **THEN** the stored body is `see https://example.com`, the link is extracted
  into the links table, and keyword search matches the flattened text.

#### Scenario: Service message

- **WHEN** a message has `type: "service"` (e.g. a pin, join, or call event)
- **THEN** it is imported with the system-message flag and rendered like other
  sources' system events.

#### Scenario: Malformed entry

- **WHEN** one chat object in `chats.list` is missing required fields or
  contains invalid JSON values
- **THEN** that chat is logged and skipped and the remaining chats import
  normally.

### Requirement: Idempotent incremental re-import

Telegram imports SHALL flow through the existing ingest pipeline invariants:
messages keyed by a stable content hash with a sequence disambiguator,
re-import of an unchanged export a no-op, and a replaced (newer) export
importing only genuinely new content. A full re-export by Telegram Desktop
MUST NOT produce duplicate messages.

#### Scenario: Re-export after new activity

- **WHEN** the user replaces the export folder with a fresh full export
  containing 500 previously imported chats plus 3 new messages
- **THEN** re-import stores exactly the 3 new messages and the conversation
  counts update accordingly.

### Requirement: Media and attachments

Attachment references in the export (relative paths under directories such as
`photos/`, `video_files/`, `voice_messages/`, `files/`, and `stickers/`)
SHALL resolve through the shared traversal-safe archive-path layer against the
telegram root. Missing media files MUST render as placeholders, not errors.
Photo attachments SHALL participate in the media gallery and the existing
image transcode pipeline.

#### Scenario: Traversal attempt in a crafted export

- **WHEN** a crafted `result.json` references `../../etc/passwd` as a media
  path
- **THEN** resolution is refused by the archive-path containment layer and the
  message imports without an attachment.

### Requirement: Reactions

Exported reactions on messages SHALL map to msgbrowse's reaction model
(badges on the target message), not standalone messages.

#### Scenario: Reaction round-trip

- **WHEN** an exported message carries a `reactions` array with two 👍 entries
- **THEN** the transcript renders a reaction badge on that message and no
  additional message rows are created.

### Requirement: Detection and guided onboarding

Setup detection SHALL report Telegram as Detected when Telegram Desktop is
present for the current user (application bundle or its application-support
data directory), and SHALL probe the well-known default export locations
(newest `ChatExport*` folder containing `result.json` under
`~/Downloads/Telegram Desktop/`) for an adoptable export. The Providers card
SHALL guide the user through the in-app export (JSON format, media included)
when Telegram Desktop is detected but no export is found, following the
SPEC-0013 guidance pattern. Adopting an export copies/stages it into the
managed root for the fixed `telegram` enum value.

#### Scenario: Detected, no export yet

- **WHEN** Telegram Desktop is installed but no export folder is found
- **THEN** the Telegram card shows the export guidance (the in-app steps) and
  a Recheck affordance, and no import runs.

#### Scenario: Export found

- **WHEN** a probed default location contains a `result.json` export
- **THEN** the card offers one-click Enable, which stages and adopts the
  export into the managed telegram root and imports it, with progress and
  logs identical to other sources.

### Requirement: No filesystem paths over HTTP

Consistent with SPEC-0013, the web layer MUST NOT accept client-supplied
filesystem paths for Telegram adoption. Folder discovery is limited to
server-side probes of well-known locations; an OPTIONAL desktop-shell native
folder picker MAY supply a path via the in-process shell seam (never via an
HTTP parameter). All Telegram setup POSTs (enable, refresh, recheck) MUST be
gated by the shared privileged-POST checks (same-origin + per-session token).

#### Scenario: Crafted POST with a path

- **WHEN** a POST to a Telegram setup endpoint includes a `path` form value
- **THEN** the value is ignored (or the request rejected) and adoption only
  ever reads server-probed or shell-seam locations.

### Requirement: Doctor and CLI coverage

`msgbrowse doctor` SHALL validate the telegram root (presence, `result.json`
parseability, media-directory heuristic) with actionable hints, and the
`import`/`sync` CLI paths SHALL accept the telegram root via flag, config, and
environment consistent with existing sources. `export`/`sync` SHALL treat
Telegram's export stage as manual (skipped with an explanatory notice), since
no exporter binary exists (ADR-0022).

#### Scenario: Doctor on a missing export

- **WHEN** the configured telegram root lacks `result.json`
- **THEN** doctor reports the finding with the exact in-app export steps as
  the hint.

### Requirement: Device-sync participation

The managed telegram root SHALL participate in Syncthing device sync exactly
like the other enum sources (SPEC-0014): folder provisioning, importer/replica
roles, and re-ingest on arrival require no telegram-specific handling beyond
enum membership.

#### Scenario: Replica receives telegram archive

- **WHEN** a paired replica receives the telegram folder from the importer
- **THEN** its watcher re-ingests it under `source = "telegram"` with the
  standard sync-status surfaces.

### Requirement: Error Handling Standards

All error-producing operations in the parser, adoption, and import paths MUST
follow structured error handling: errors wrapped with context at layer
boundaries, sentinel errors for failure modes callers distinguish (e.g.
export-not-found vs. malformed-export), no silent swallowing (every error
returned, logged with context, or explicitly suppressed with a documented
reason), and structured key-value logging. Log lines MUST NOT contain message
bodies or personal content — counts, ids, and paths only.

#### Scenario: Malformed export surfaces honestly

- **WHEN** `result.json` fails to parse at the top level
- **THEN** Enable fails with a sentinel-classified error, the Providers card
  shows the guidance state (not a generic failure), and the raw parse error is
  available in Settings → Logs.

### Requirement: Database Operation Standards

Telegram imports MUST use the existing transactional ingest path: each
conversation's replacement is atomic within a transaction, queries are
parameterized, and the allowed-source migration ships as a versioned schema
migration in its own transaction.

#### Scenario: Interrupted import

- **WHEN** an import is cancelled mid-conversation
- **THEN** the database contains either the conversation's prior state or its
  complete new state, never a partial mix.

## Security Requirements

This spec is web-facing (Providers/setup endpoints). Per the house posture
(ADR-0010, SPEC-0013): the server binds loopback only and is single-user;
authentication is the loopback trust boundary itself.

- **Authentication:** All Telegram setup endpoints inherit the shared
  privileged-POST gate (same-origin verification + 256-bit per-session token,
  constant-time compare). Auth designation: every mutating endpoint below is
  gated; read-only fragments are same-origin loopback reads.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | /setup/telegram/enable | Required (setup gate) | Stage + adopt + import the probed export |
| POST | /setup/telegram/refresh | Required (setup gate) | Re-probe and import what's new |
| POST | /setup/telegram/recheck | Required (setup gate) | Re-run detection probes |
| GET | /setup/telegram/status | Loopback read | Progress fragment (poll) |

- **Rate limiting:** Not applicable beyond the existing per-source
  single-job guard (a second Enable/Refresh while one runs is a no-op) —
  loopback single-user posture, consistent with all other sources.
- **Security headers:** Inherited app-wide (strict CSP `default-src 'none'`,
  nosniff, no-referrer, frame denial). This spec introduces no inline
  styles/scripts.
- **Request body size limits:** Setup POSTs are wrapped in the existing
  small `MaxBytesReader` cap (4 KiB).
- **CSRF protection:** The setup gate's Origin/Sec-Fetch-Site checks plus the
  per-session token are the CSRF defense; no cookies are used.
- **Redirect validation:** Setup responses render fragments or redirect only
  to fixed same-origin paths; no client-influenced redirect targets.

## Accessibility Requirements

This spec involves user-facing UI (the Providers card and guidance). The
following are MANDATORY per WCAG 2.1 AA, consistent with the existing
Providers surfaces:

- **WCAG 2.1 AA Compliance:** the Telegram card, guidance modal, and progress
  states MUST meet WCAG 2.1 AA.
- **ARIA Landmarks:** the card renders inside the existing `role="main"`
  content region; the guidance modal is a labelled dialog.
- **Icon-Only Controls:** any icon-only control (refresh icon, close) MUST
  carry `aria-label`.
- **Dynamic Content Regions:** import progress updates MUST use
  `aria-live="polite"`, matching the existing setup progress fragments.
- **Keyboard Navigation:** Enable/Recheck/guidance controls MUST be
  keyboard-operable; Escape dismisses the guidance modal.
- **Focus Management:** the guidance modal MUST trap focus while open, focus
  its first actionable element on open, and return focus to the invoking
  control on close.
