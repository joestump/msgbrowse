---
title: Full-text search
sidebar_label: Search
sidebar_position: 2
---

# Full-text search

msgbrowse indexes every message body into SQLite's built-in FTS5 engine at
import time, so keyword search over years of history is instant and works
entirely offline — no LLM, no embeddings, and no network access required.

## Live results

The search page updates as you type: HTMX re-fetches the results list (with a
short debounce) on every input or filter change. The form also degrades
gracefully — without JavaScript it submits as a plain GET and the results
render server-side.

Results are ranked by BM25 relevance. Each result card shows the
conversation, sender, timestamp, and a source pill (Signal, iMessage, or
WhatsApp), plus
paperclip/link glyphs when the message carries an attachment or a link.
Clicking a result jumps to that exact message in its transcript, centered with
surrounding context and visually highlighted (see
[jump-to-context anchors](./browsing.md#jump-to-context-anchors)).

## Filters

Every filter is optional and composable with the query:

| Filter | Behavior |
| --- | --- |
| Conversation | Restrict to one conversation. |
| Source | `signal` or `imessage`. |
| Sender | Exact sender name match. |
| From / To | Inclusive date bounds (`YYYY-MM-DD`). |
| Has attachment | Only messages with at least one attachment. |
| Has link | Only messages containing a link. |

## Query behavior

msgbrowse turns your input into a safe FTS5 expression: each
whitespace-separated word becomes a quoted **prefix** term, and all terms are
ANDed. So `dog park` matches messages containing a word starting with "dog"
AND a word starting with "park". Quoting every token neutralizes FTS5
operators and punctuation, so pasted text can never produce a syntax error or
change the query's structure.

## Snippets and highlighting

Each hit shows a short excerpt of the message body with the matched terms
highlighted. Highlighting is done safely: the store layer marks matches with
control-character sentinels (never HTML), the web layer HTML-escapes the
untrusted message text first, and only then swaps the sentinels for `mark`
tags. Message content is never rendered as raw HTML.

## Semantic search

The web search page is keyword-only today. Meaning-based (vector) search is
available through the [MCP server](./mcp-server.md) — the `semantic_search`
tool and the hybrid `search_messages` tool — after you compute embeddings:

```sh
msgbrowse --data-dir ./data embed
```

See [AI features](./ai-features.md#embeddings-and-semantic-search) for how
embeddings work and what they send to your configured LLM endpoint.

:::tip
Keyword search needs no configuration and no LLM endpoint — it works the
moment your first import finishes.
:::
