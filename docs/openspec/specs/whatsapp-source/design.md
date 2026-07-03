# SPEC-0009 Design: WhatsApp source

- **Capability:** whatsapp-source
- **Related ADRs:** [ADR-0016](../../../adr/0016-whatsapp-source-exporter.md), [ADR-0003](../../../adr/0003-dual-source-archive.md), [ADR-0010](../../../adr/0010-security-privacy-posture.md), [ADR-0015](../../../adr/0015-onboarding-doctor-export-sync.md)

## Architecture

WhatsApp is the third tenant of the multi-source machinery — every box below
except `internal/whatsapp` already exists and gains only a `case`:

```
msgbrowse export ──▶ whatsapp-chat-exporter (pipx tool, ADR-0016)
                        │  reads iOS backup / Android crypt DB (outside msgbrowse)
                        ▼
        <whatsapp_archive_root>/          ← read-only to msgbrowse
          result.json (or per-chat JSON)  ← exact layout pinned vs fixtures
          <media dirs copied by the tool>
                        │
msgbrowse import ──▶ internal/whatsapp.Run(store, Options)   (mirrors imessage.Run)
                        │  JSON → signal.Message stream:
                        │    TimestampRaw = epoch → signal.TimestampLayout (REQ-0009-004)
                        │    Reactions    → []signal.Reaction (REQ-0009-005)
                        │    media refs   → Attachments (root-relative RelPaths)
                        ▼
                unified store (source='whatsapp'; NO schema change)
                        │
web/media: archivepath.Resolve(source, roots…) gains the whatsapp branch
web/UI:   sourceSlug → 'src-whatsapp' (presence dot + pill; input.css tokens)
contacts: phone-keyed contact_identifiers merge — free (existing machinery)
```

## Key decisions

- **JSON over text** (ADR-0016): the parser is a field mapping, not a grammar.
  The upstream schema is unversioned, so the contract is defended by
  **committed fixtures from a real sanitized export**; the concrete field
  table lives here (below) and is filled in by the foundation story when the
  first fixture lands. The parser ignores unknown fields and skip-logs
  malformed entries (ParseError parity).
- **Timestamps**: the export carries epoch timestamps; `TimestampRaw` is
  `time.Unix(...).Format(signal.TimestampLayout)` from day one. The render
  fallback added for legacy iMessage rows (#81) must never be needed for
  WhatsApp rows — a test asserts canonical output on fixtures.
- **Hashing/identity**: message hash inputs (conversation, ts_raw, sender,
  body, seq) are unchanged; WhatsApp rows are new so there is no re-key
  concern. Conversation naming follows the export's chat naming (phone number
  or group subject); phone-named chats merge onto contacts exactly like
  iMessage numbers, and the `initials()`/`humanName()` phone handling from
  #81 applies unchanged.
- **Media**: the tool copies media beneath its output dir; RelPaths are stored
  root-relative and served through the existing `/media/{conv}/{path}` route,
  so traversal containment and HEIC/TIFF transcoding come for free. Voice
  notes (`.opus`) and stickers (`.webp` animated) render as file chips/images
  respectively in slice one — no transcription, no special casing.
- **Platform**: iOS vs Android changes only the *backup prerequisite* the user
  performs before running the exporter. Doctor prints both remediation paths;
  docs describe both; parsing is identical. (Owner's platform is an open
  question on the epic — it decides which doc path gets written first, not
  the architecture.)

## Field mapping (pinned)

Pinned by the foundation story (#89) against the exporter's own serialization
(`data_model.py` `Message.to_json()` / `ChatStore.to_json()`, plus
`ios_handler.py` / `android_handler.py` field assignments) and the committed
fixtures in `internal/whatsapp/testdata/`. The export is one `result.json`: a
top-level object keyed by chat JID; each chat object carries `name`, `type`,
`media_base`, avatar/status fields, and a `messages` object keyed by database
row id.

| store field | exporter JSON source | notes |
|---|---|---|
| conversation name | chat `name`, else JID local part | `name` is the contact name or group subject; a null name falls back to the JID's local part (the phone number). Display-name collisions get the JID local part appended (`"Name (1555…)"`) so distinct JIDs never merge. |
| group detection | chat key suffix `@g.us` | 1:1 chats end `@s.whatsapp.net` (or `@lid`); the chat `type` field is the DEVICE ("ios"/"android"), not the chat kind. |
| sender | `from_me`, `sender` | `from_me:true` → `signal.OwnerSender` ("Me"). Group messages carry the member name/number in `sender`; 1:1 messages leave it null and map to the conversation name. |
| IsSystem | `meta` (+ `media`, `data`) | `meta:true` without media (group renames, deleted messages, calls) → `signal.SystemSender` + IsSystem. `meta` is ALSO set on missing-media and vCard rows, which stay regular messages. A `data:null`, media-less row (unsupported internal message — polls, calls, unsynced media on companion exports) is kept as an empty system event, mirroring the exporter's own rendering. |
| ts_unix / TimestampRaw | `timestamp` | The epoch-seconds field (`Message.__init__` normalizes ms→s; floats truncate). `TimestampRaw = time.Unix(epoch).In(loc).Format(signal.TimestampLayout)` at parse time — canonical from day one (REQ-0009-004). The pre-formatted `time` (HH:MM), `received_timestamp`, and `read_timestamp` strings are ignored. `loc` defaults to local time, matching the wall-clock convention of the other sources. |
| body | `data`, `caption` | Text messages: `data`, with the exporter's `<br>` newline substitution undone; exporter-injected anchors collapse to their labels, but user text is never tag-stripped/unescaped. Media messages: `caption` (empty when absent). The missing-media sentinel (`data:"The media is missing"`, `mime:"media"`) never becomes body text. |
| Attachments.RelPath | `media_base` + `data` (when `media:true`) | Full path = `media_base` + `data` (the exporter's own `<base href>` semantics). Stored root-relative ONLY (the iMessage absolute-path lesson): absolute paths under the archive root are relativized; absolute paths elsewhere fall back to the relative `data` part; a last-resort basename beats persisting a foreign absolute path. Kind: `mime` prefix `image/` → image (stickers included), else file chip (voice notes, PDFs). Missing media keeps a pathless attachment labeled with the sentinel so the chip fallback renders. |
| vCard messages | `mime:"text/x-vcard"`, `data` | `data` is exporter HTML (`…vCard file(s):<br><a href="….vcf">Name</a>`); anchors become file attachments (label = contact name, href relativized) and the prose is stored as plain text — markup never reaches the body. |
| Reactions | `reactions` object `{actor: emoji}` | → `[]signal.Reaction`, ordered by actor for determinism; the exporter's own-reaction actor `"You"` maps to `signal.OwnerSender`. Reaction text never lands in bodies (REQ-0009-005). |
| quoted replies | `reply`, `quoted_data` | `reply` is the quoted parent's `key_id`, `quoted_data` its text; neither merges into the replying body (the parent is its own message). Thread affordances are a possible follow-on. |
| ignored | `key_id`, `safe`, `thumb`, `message_type`, avatars, `status`, `my_avatar` | Unknown/unneeded fields are ignored per REQ-0009-003. `message_type` semantics (6=metadata, 14=deleted, 15=sticker on iOS) are informational — the `meta`/`sticker`/`mime` flags already carry the decision. |

Message ordering: `messages` object keys are database row ids; entries sort by
epoch timestamp with numeric-key tie-break (original database order), which is
what makes re-parses deterministic (Go maps are unordered). Malformed chats
and messages (missing/invalid `timestamp`, missing `from_me`, non-object
entries) are skip-logged via `whatsapp.ParseError` and never abort the rest of
the chat.

## Non-goals (this spec)

- Native per-chat "Export chat" `.txt` parsing (manual, reaction-less,
  locale-string timestamps — ADR-0016 option 2). Revisit only if a real
  archive cannot use the backup route.
- Reading live WhatsApp databases or decrypting backups inside msgbrowse.
- Voice-note transcription (existing `llm.transcribe` machinery could adopt
  it later, separate spec).

## Testing & verification

- Parser: fixture-driven golden tests (messages, groups, reactions, media
  refs, malformed-entry skip, canonical timestamps); property: re-parse
  idempotence.
- Ingest: incremental no-op on unchanged root; changed-chat replacement
  without duplication; reactions rebuilt on re-ingest.
- Media: traversal rejection under the new root; renderability checks for
  webp/jpeg; transcode path exercised for HEIC-in-WhatsApp (rare but real).
- Doctor: table-driven checks for missing root / missing JSON / missing media
  / missing exporter, each with its remediation string.
- Web: source pill + presence dot for `src-whatsapp` (CSS assertions per the
  #84 pattern), identifier chips show merged handles.
