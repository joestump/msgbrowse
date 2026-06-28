# SPEC-0006 Design: Web UI redesign (slate)

- **Capability:** web-ui-redesign
- **Related ADRs:** [ADR-0012](../../../adr/0012-slate-redesign-design-system.md), [ADR-0007](../../../adr/0007-frontend-styling-tailwind-daisyui.md), [ADR-0006](../../../adr/0006-web-stack-htmx.md)
- **Design source:** [docs/design/redesign-handoff.md](../../../design/redesign-handoff.md), `docs/design/msgbrowse-redesign.dc.html`

## Architecture

The redesign is presentation-layer first: the data model, routes, and store
methods are reused. New work is concentrated in `internal/web/templates/*`,
`internal/web/render.go`, `internal/web/tailwind/input.css`, and the daisyUI
theme declaration in `input.css`; a few screens need small `internal/store`
additions.

```
internal/web/tailwind/input.css ── @plugin daisyui { themes: slate --default, slate-light; }
                                 └─ custom CSS: .dense-log, .stat-strip, .result-card,
                                    .editorial-card, .source-pill, .presence-dot, …
internal/web/templates/         ── partials (navbar, sidebar, rows), conversation (dense log),
                                    index (home), search, gallery (media), status, journal
internal/web/render.go          ── avatarColor (new palette), presenceColor/sourceLabel,
                                    relativeTime, group-consecutive helpers
internal/store/                 ── pinned column (migration); OnThisDay query; recent-with-
                                    relative-time; search elapsed measured in the handler
```

## Key design decisions

### Theming: custom daisyUI themes, variables drive bespoke CSS
`input.css` declares two daisyUI custom themes — `slate` (default, dark) and a
derived `slate-light` — mapping daisyUI's semantic variables (`--color-base-100`,
`--color-primary`, …) to the slate tokens. Bespoke components read those
variables so both variants work from one rule set. The header toggle (ADR-0007's
`theme.js`) switches `data-theme` between `slate` and `slate-light`. daisyUI's
stock `dim`/`winter` are dropped. The dynamic avatar palette is safelisted via
`@source inline(...)` exactly as today.

`slate-light` is **derived, not from the brief** (ADR-0012): light surfaces,
`#6f93d6`-family accent kept, text inverted to dark-on-light. It is explicitly a
best-effort variant.

### Dense-log transcript replaces chat bubbles
The transcript template moves from daisyUI `chat`/`chat-bubble` to a CSS grid
row: `[timestamp gutter ~76px] [3px rail] [content ≤640px]`. Rail color = the
sender's avatar color, or the accent for "Me"; "Me" rows add a faint accent wash
+ accent left edge. Day separators and system events are inline non-message rows
(reusing the existing `IsSystem` flag). Consecutive same-sender messages within a
short window are grouped (suppress the repeated name/avatar; keep timestamps).
The existing quote-block rendering (`renderBody` → `.msg-quote`) is retained;
attachment chips and link-preview pills reuse the existing attachment/link data.
Existing web tests asserting `chat-bubble` markup are rewritten to assert the
dense-log structure.

### Paging / virtualization
Keep the current HTMX keyset infinite scroll (`/c/{id}/messages?after_ts…`).
True row virtualization is optional and out of scope for the first pass; the
keyset pager already bounds DOM growth per request.

### Backend additions (small, per issue)
- **Pinned:** schema migration adds `conversations.pinned INTEGER NOT NULL
  DEFAULT 0`; `ListConversations` returns it; a pin/unpin handler toggles it.
  This is migration v5 (after the contact-facts v4), or v6 if the pure-Go SQLite
  work lands a migration first — the runner is append-only, so the slice picks
  the next free version at implementation time.
- **On this day:** a store query for messages whose month/day match today in a
  prior year, newest prior year first.
- **Recent + relative time:** `ListConversations` already orders by last
  activity; a `relativeTime` template helper renders `2m / 1h / yesterday`.
- **Search elapsed:** measured in the search handler around `SearchMessages` and
  passed to the template (no store change).

### Safety / constraints carried over
All untrusted content keeps flowing through `renderBody`/`html/template` escaping
(ADR-0010); no inline JS (the lightbox stays CSS `:target`); Heroicons inline
SVG; no CDN; `app.css` rebuilt from a clean `.tools` cache before commit (the CSS
drift guard).

## Sequencing & dependencies

Below, the bare `REQ-NNN` form abbreviates the spec's `REQ-0006-NNN` IDs.

```
REQ-001 (theme foundation)
   ├─► REQ-002/003 (navbar, sidebar)  ─► REQ-004 (avatars/presence/pills)
   │        └─► REQ-010 (pinned: needs migration)
   ├─► REQ-005/006 (dense-log transcript + header)
   ├─► REQ-007 (home: on-this-day + recent queries)
   ├─► REQ-008 (search: elapsed)
   ├─► REQ-009 (media)
   └─► REQ-011 (status re-skin)
REQ-012 (journal UI) ── BLOCKED on the journal digest backend (Slice 6)
```

## Testing strategy
- Per-screen render tests in `internal/web` assert the new structure (gutter,
  rail, source pill, stat strip, result card, tabs) and the absence of
  `chat-bubble`.
- `render.go` helper unit tests: avatar palette mapping, `relativeTime`,
  consecutive-grouping, `sourceLabel`/presence color.
- Store unit tests for the pinned migration + `OnThisDay`.
- The CSS drift guard (`.github/workflows/css.yml`) continues to gate `app.css`.
