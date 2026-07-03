---
title: Browsing conversations
sidebar_label: Browsing
sidebar_position: 1
---

# Browsing conversations

msgbrowse renders your Signal and iMessage archives as a fast, local,
server-rendered web UI. Everything happens on your machine: the pages are Go
templates over your local SQLite database, progressively enhanced with HTMX,
and served on loopback by default. No CDN, no external fonts or scripts, no
tracking.

Start the UI with:

```sh
msgbrowse --data-dir ./data serve
```

`serve` binds to `127.0.0.1:8787` by default and opens your browser
automatically (use `--open=false` for headless runs). Use `--host` and
`--port` — or `--listen-addr` to replace the whole address — to bind elsewhere.

:::warning
The UI has no authentication. It is loopback-only by default on purpose; only
bind to a non-loopback address behind your own access control (a VPN, an
authenticating reverse proxy, an SSH tunnel).
:::

## The sidebar

The sidebar lists every conversation — person or group, across both sources —
with the message count and a one-line preview of the most recent message and
its sender.

- **Live filter.** The "Filter conversations" box narrows the list as you
  type, entirely client-side. It filters both the pinned and unpinned sections
  at once.
- **Pinning.** Open a conversation and click **Pin** in its header to float it
  into a dedicated **Pinned** section at the top of the sidebar. Pins are
  stored in the database (a per-conversation flag), so they survive restarts
  and re-imports. Click **Unpin** to drop it back into the main list.

## The dense-log transcript

Conversations render as a dense, log-style transcript designed for scanning
years of history quickly:

- **Day separators.** A labeled divider is emitted whenever the calendar date
  changes between consecutive messages.
- **Sender rails and grouping.** Each message row is a timestamp gutter, a
  sender-colored vertical rail, and the content column. When the same sender
  posts several messages in a row within a day, the sender name is shown only
  on the first message of the run. Your own messages ("Me") carry a subtle
  accent wash and accent-colored rail.
- **System events.** Timeline events (member changes, calls, and similar)
  render centered and italic, without a rail or gutter.
- **Reaction badges.** Emoji reactions appear as compact badges under the
  message, with a count when more than one person used the same emoji; hover a
  badge to see who reacted.
- **Attachments and links.** Renderable images appear as inline thumbnails;
  files (and images with no browser-renderable rendition) appear as labeled
  attachment chips. Links in a message body get a domain pill.
- **Infinite scroll.** The transcript loads incrementally as you scroll —
  HTMX fetches the next page of messages when the loading row scrolls into
  view.

## Jump-to-context anchors

Every message has a stable anchor (`#m<id>`), and search results, gallery
items, and AI contact facts all deep-link to
`/c/<conversation>/at/<message>` — a transcript view centered on that exact
message, with a window of surrounding context loaded on each side. The target
message is visually highlighted, and infinite scroll continues downward from
there, so a search hit is always two clicks from its full conversation.

## Configuration

| Key | Default | What it shapes |
| --- | --- | --- |
| `listen_addr` | `127.0.0.1:8787` | The bind address for `serve` (loopback by default). |
| `ingest_on_start` | `false` | Run an import pass when `serve` boots, so the UI is fresh. |

Both keys live in `config.yaml` and have `MSGBROWSE_LISTEN_ADDR` /
`MSGBROWSE_INGEST_ON_START` environment equivalents; the `serve` flags
(`--listen-addr`, `--host`, `--port`) override them.

:::tip
Looking for a specific message instead of a specific conversation? Head to
[Search](./search.md) — every result links back into the transcript at the
matching message.
:::
