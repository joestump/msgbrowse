# Handoff: msgbrowse — Redesign

## Overview
**msgbrowse** is a local-only desktop app for reading, searching, and browsing your entire personal messaging archive (Signal, iMessage, etc.) — 100k+ messages across 100+ conversations, imported and kept on-device. The redesign covers the full app: a home landing, a conversation reader (transcript), full-text search, a media browser, and a "Journal" feature that editorializes individual days.

The tagline that anchors the product voice: *"A calm, private reading room for everything you've ever said. Local-only — nothing leaves this machine."*

## About the Design Files
The file in this bundle (`msgbrowse Redesign.dc.html`) is a **design reference created in HTML** — a prototype showing the intended look, layout, and behavior. It is **not production code to copy directly.** Your task is to **recreate these designs in msgbrowse's actual codebase**, using its established framework, component library, and patterns. If the project has no front-end environment yet, pick the most appropriate framework for a local desktop app (e.g. a Tauri/Electron + React/Svelte renderer) and implement there.

`support.js` is only the harness that lets the `.dc.html` reference open in a browser for review — **ignore it for implementation.**

### How to view the reference
Open `msgbrowse Redesign.dc.html` in a browser. It renders on a pannable canvas with several frames laid out left-to-right / top-to-bottom:
- **Direction A · Reading Room**, **Direction B · Index**, **Direction C · Editorial** — three early *explorations* of visual tone. These are NOT the target; they show the options that were considered.
- **★ Selected Direction** — the chosen synthesis: A's people-first sidebar + B's dense slate transcript + C's editorial layer. **This is the design language to build.**
- **Home**, **Search**, **Media**, **Journal** — the app screens, all already in the selected (slate) style.

## Fidelity
**High-fidelity.** Colors, type sizes, spacing, and layout in the *Selected Direction* and the four app screens are final. Recreate them pixel-faithfully using your codebase's components. (Directions A/B/C are exploratory and should NOT be built — they're context only.)

---

## Design Tokens (Selected Direction — the slate system)

### Color
| Role | Value |
|---|---|
| App background (base) | `#0f1216` |
| Raised panel / card | `#13171c` |
| Navbar / sidebar surface | `#161a20` |
| Hover / selected row tint | `#1b2330` / `rgba(111,147,214,.05–.12)` |
| Border (primary) | `#232a33` |
| Border (subtle / dividers) | `#1c2128`, `#1c222a`, `rgba(35,42,51,.55)` |
| Text primary | `#dbe2ea` (headings sometimes `#eaf1fb`) |
| Text secondary | `rgba(219,226,234,.5)` – `.7` |
| Text dim / meta | `rgba(219,226,234,.32)` – `.42` |
| **Accent (slate blue)** | `#6f93d6` |
| Accent (light, "Me" name) | `#9bb6e6` |
| Accent on accent-bg text | `#0f1216` (dark text on `#6f93d6` buttons) |
| Search highlight `<mark>` | bg `rgba(232,184,75,.28)`, text `#f0d28a` |
| Source pill — Signal | text `#6f93d6`, border `rgba(111,147,214,.35)` |
| Source pill — iMessage | text `#7bd88f`, border `rgba(52,199,89,.3)` |
| Presence dot — Signal | `#3b82f6` |
| Presence dot — iMessage | `#34c759` |

### Avatar colors (assign deterministically by person, e.g. hash of name → index)
`#f43f5e` · `#0ea5e9` · `#f59e0b` · `#14b8a6` · `#d946ef` · `#f97316` · `#6366f1` · `#10b981`
Avatar = colored circle (or 6px-radius rounded square in dense views), white initials, 600 weight. A small presence dot (Signal/iMessage color) sits bottom-right with a 2px border matching the surface behind it.

### Typography
- **Sans (UI):** system stack — `-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif`. Antialiased.
- **Mono (numerals, timestamps, filenames, counts):** `ui-monospace, SFMono-Regular, Menlo, monospace`. Use `font-variant-numeric: tabular-nums` on all counts even in the sans face.
- Type scale in use (px): meta/timestamps 10.5–12 · body/rows 13–14.5 · row titles 14 · screen H1 21–26 · home hero 38. Weights: 400 body, 500–600 names/labels, 700 headings/hero. Tighten headings with `letter-spacing: -.01em` to `-.025em`.

### Shape, spacing, elevation
- Border radius: rows/cards 8–14px, inputs 8–11px, avatars 50% (or 6px in dense list), pills 5–8px, full app frame 14–16px.
- App frame shadow (review only): `0 24px 70px rgba(0,0,0,.45)`.
- Navbar height 54px. Sidebar width 320px. Transcript text column max-width 640px (680px for editorial).
- Use fl/grid with `gap`, not margins, for rows and toolbars.

---

## Screens / Views

### 1. App shell (every screen)
- **Navbar (54px):** home/logo glyph in accent `#6f93d6`, wordmark **msgbrowse** (16px/700), spacer, right-aligned global count in dim mono — `107 conversations · 105,406 messages`, then a 34px circular settings (gear) button outlined in `#232a33`.
- **Sidebar (320px, surface `#161a20`):**
  - Filter input (36px, bg `#0f1216`, border `#232a33`, search glyph at left) — placeholder "Filter conversations".
  - Nav links with leading icons: **Search · Media · Journal · Status & backups** (8px row padding, 8px radius, hover tint).
  - **PINNED** section (10.5px tracked uppercase label) — 2-line conversation rows for pinned people.
  - **CONVERSATIONS** section header with a right-aligned count (`107`), then a scrolling list. Selected row: bg `#1b2330` + a 2–3px accent rail on the left edge.
  - Conversation row (2-line): avatar+presence · name (600) · right-aligned message-count in dim mono · second line = last-message preview (`Me: …` prefix when you sent it), single-line truncated.

### 2. Conversation reader (transcript) — *Selected Direction main panel*
The chosen transcript is the **dense log** style (from Direction B), not chat bubbles.
- **Header:** avatar + name (22–26px/700), inline pill chips for phone/email (`+1 415 555 0148`, `chelsea@stump.rocks`), then a meta line: `38,593 messages · Aug 2021 – Jun 2026` plus icon+count chips for images / links / attachments.
- **Message row:** a left **timestamp gutter (~76px, mono `HH:MM:SS`, dim)**, then a **3px colored rail** (sender's avatar color for them; accent `#6f93d6` for you), then the content column (max-width 640px): sender name (12.5px/600 — "Me" rendered in `#9bb6e6`) above the message body (14px, line-height 1.55).
- **Quoted link/attachment:** body can contain a left-border quote block (dim), an attachment chip (paperclip icon + mono filename + size, e.g. `signal-2022-10-21-201713.mp4 · 4.2 MB`), and a link-preview pill in accent.
- **Image placeholder:** a 200×135 striped block (`repeating-linear-gradient(135deg, #1a1f26 0 9px, #161a20 9px 18px)`, 1px `#232a33` border) with the mono filename centered. **Replace with the real decoded image in production.**
- **Day separator:** centered uppercase date label flanked by hairlines (`October 22, 2022`).
- **System events:** centered italic/mono dim text, e.g. `— Chelsea shared 2 photos —`.
- **"Me" row** gets a faint accent wash background (`rgba(111,147,214,.05)`) + accent left edge to distinguish your messages.

### 3. Home (landing)
Centered ~780px column on base bg:
- Hero: **msgbrowse** (38px/700) + the two-line tagline (secondary color).
- **Stat strip:** one bordered rounded bar split into 3 cells with hairline dividers — *Conversations 107 · Messages 105,406 · Newest message Jun 27, 2026 · 9:17 PM* (labels 10.5px tracked uppercase dim, values 28px/700 tabular).
- **2-col grid:** *On this day · 4 years ago* card (calendar icon, a quoted resurfaced message, avatar+name+date, "Open that day →" accent link) **+** *Jump back in* card (recent conversations list with relative times `2m / 1h / 3h / yesterday`).
- **4-up quick-links grid:** Search (Full-text, filtered) · Media (4,373 images) · Journal (Editorialized days) · + one more. Each is a bordered card with accent icon, title, dim subtitle.

### 4. Search
- Large search field (48px, value e.g. `gunsticles`) with leading glyph.
- **Filter row (wraps):** dropdown chips *All conversations · All sources · From · To*, a *Sender* text input, two toggle chips *Has attachment* (active = accent fill check) / *Has link*, spacer, and a solid accent **Search** button (dark text on `#6f93d6`).
- Results meta: `248 results · 0.04s` (dim mono).
- **Result card:** header line = avatar · conversation name (600) · sender · timestamp (`2022-10-22 4:17 AM`), right side = source pill (Signal/iMessage) + optional link/attachment glyph. Body = snippet with the matched term wrapped in the highlight `<mark>`. The top/active result has an accent border (`rgba(111,147,214,.5)`).

### 5. Media
- H1 **Media** + tabs **Images / Files / Links**, each with a count badge (`4,373 / 640 / 4,570`); active tab has a 2px accent underline + accent badge.
- Filter row: *All conversations · All sources · Any date* dropdown chips.
- **Images:** 5-col grid of square (`aspect-ratio:1`) cover tiles, 9px radius, with a bottom gradient scrim and mono filename. Tiles use the striped placeholder — swap for real thumbnails. Clicking a tile opens a lightbox (a CSS/`:target` lightbox is acceptable; in production wire it to your image viewer).

### 6. Journal (headline feature)
Centered ~880px column:
- Header: **Journal** label (accent, icon) + actions *On this day* / *Random day* (bordered chips).
- **Daily editorial card** (`#1a1d21`-ish raised, 16px radius, 1px border): an accent eyebrow *"This day, editorialized"* (sparkle icon), a 1–2 sentence AI-written prose summary of that day's threads (15.5px, line-height 1.6), then a meta row of *People · … / Themes · … / Mood · …* (mood value in accent). This is generated narrative over the raw messages — treat it as a content/LLM feature, not static copy.
- A month strip and per-day cards continue below (highlights / people / themes / mood / media / links per day).

---

## Interactions & Behavior
- **Navigation:** sidebar conversation rows open the transcript for that conversation; nav links route to Search / Media / Journal / Status; Home quick-links and "Open that day →" route accordingly.
- **Selected state:** active sidebar row = `#1b2330` bg + accent left rail. Active tab/filter = accent underline or accent fill.
- **Search:** live filtering — typing + filter chips + toggles refine results; matched terms highlighted via `<mark>`. Show result count + query time.
- **Hover:** rows and cards lift to a subtle tint (`rgba(111,147,214,.05–.08)` or `#1b212a`); links/icons brighten toward primary text color.
- **Media lightbox:** clicking an image opens a full-size overlay (prev/next within the current tab/filter set).
- **Transcript:** infinite/virtualized scroll over very large histories (tens of thousands of messages) — virtualize the list. Day separators and system events are inline non-message rows. Auto-group consecutive messages from the same sender within a short window.
- **Presence/source:** the dot color and source pill are derived from the message's source (Signal = blue `#3b82f6`, iMessage = green `#34c759`).

## State Management
- `conversations[]` (id, name, avatarColor, source, phone, email, messageCount, lastMessage, pinned, firstSeen/lastSeen) and selected conversation id.
- `messages[]` per conversation (id, sender/isMe, timestamp, body, attachments[], links[], systemEvent?) — paginated/virtualized.
- Search state: query string, filters (conversation, source, sender, from/to date, hasAttachment, hasLink), results[], count, elapsed.
- Media state: active tab (images/files/links), filters, grid items, lightbox open + index.
- Journal state: selected day, generated editorial summary (cache the LLM output per day).
- Global counts (conversations, messages, newest message) for navbar + home.
- All data is **local-only**; no network egress. Reflect that in any settings/empty-state copy.

## Design Tokens recap (for quick reference)
- Spacing rhythm: 8 / 10 / 12 / 14 / 18 / 22 / 26 / 30 px.
- Radius: 6 (dense avatar) / 8–9 (rows, tiles) / 11–14 (cards, inputs) / 16 (editorial card) / 50% (avatars).
- Shadows: app frame only (review). In-app cards use borders, not shadows.
- Numerals always tabular; timestamps/filenames/counts in mono.

## Assets
- **No real images ship in this prototype.** Every photo/thumbnail/attachment is a striped CSS placeholder with a mono filename label. In production, render the actual decoded media from the local archive.
- **Icons:** Heroicons (outline, 1.5–1.6 stroke). Used: home, gear/cog, magnifying-glass (search), photo (media), bars-3-bottom (status), sparkles (journal), calendar, link, paperclip, chevron-down. Use your codebase's existing icon set if it has one; otherwise Heroicons match 1:1.
- **Fonts:** system sans + system mono — no web fonts to bundle.

## Files
- `msgbrowse Redesign.dc.html` — the full design reference (explorations A/B/C + Selected Direction + Home/Search/Media/Journal screens).
- `support.js` — review harness only; **not** for implementation.

> Build the **Selected Direction** + the four app screens. Directions A, B, and C are exploratory context — do not implement them.
