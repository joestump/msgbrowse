---
title: MCP server
sidebar_label: MCP server
sidebar_position: 6
---

# MCP server

`msgbrowse mcp` runs a Model Context Protocol server over your archive, so an
MCP client — Claude Desktop, Claude Code, or anything else that speaks MCP —
can answer natural-language questions about your message history. The server
is strictly **read-only**, and every tool returns exact provenance
(conversation, sender, timestamp, `message_id`), so answers are
citation-faithful: any claim can be traced back to the source message.

## Running it

Stdio is the default transport, so MCP clients can launch the binary directly:

```sh
msgbrowse --data-dir /absolute/path/to/data mcp
```

Pass `--http` to serve streamable HTTP instead, bound to `--listen-addr`
(default `127.0.0.1:8788`):

```sh
msgbrowse --data-dir /absolute/path/to/data mcp --http --listen-addr 127.0.0.1:8788
```

Logs go to stderr, so they never corrupt the stdio JSON-RPC stream.

:::tip Run `embed` first
Semantic search embeds your query via `llm.base_url` and matches it against
stored message vectors. Run `msgbrowse embed` before connecting a client so
those vectors exist — see [AI features](./ai-features.md#embeddings-and-semantic-search).
Without embeddings, `search_messages` degrades gracefully to keyword-only
results, and `semantic_search` returns an error.
:::

## Connecting Claude

Add to `claude_desktop_config.json` (or the Claude Code MCP settings). Use the
absolute path to the binary (e.g. `/home/you/go/bin/msgbrowse`) if it is not
on the client's `PATH`:

```json
{
  "mcpServers": {
    "msgbrowse": {
      "command": "msgbrowse",
      "args": ["--data-dir", "/absolute/path/to/data", "mcp"]
    }
  }
}
```

Or via Docker (stdio, reusing the compose data volume):

```json
{
  "mcpServers": {
    "msgbrowse": {
      "command": "docker",
      "args": ["compose", "-f", "/absolute/path/to/msgbrowse/docker-compose.yml",
               "run", "--rm", "-T", "msgbrowse", "mcp"]
    }
  }
}
```

Then ask things like *"what did MJ say about the lease?"* or *"summarize my
thread with Harper about the trip."*

## The tools

| Tool | What it does |
| --- | --- |
| `list_conversations` | List every conversation (person or group) with message counts, date ranges, and image/file/link counts. |
| `get_conversation` | Retrieve a conversation's transcript in chronological order. Parameters: `name` (required), `start`/`end` dates, `limit` (default 100, max 500). |
| `search_messages` | **Hybrid** keyword + semantic search across all messages, returning ranked snippets with provenance. Filterable by `conversation`, `sender`, `source` (`signal` or `imessage`), and `start`/`end` dates; `limit` defaults to 20 (max 100). |
| `semantic_search` | Pure vector (meaning-based) search over message embeddings; returns the `k` most similar messages (default 20, max 100) with provenance and a similarity score. Filterable by `conversation` and `source`. |
| `get_context` | Return the messages surrounding a given `message_id` (`window` on each side, default 5, max 50) — for assembling RAG context around a search hit. |
| `list_media` | List image or file attachments, filterable by `conversation`, `kind` (`image` or `file`), and `source`. |
| `list_links` | List deduplicated links, filterable by `domain`, `conversation`, and `source`. |

### How hybrid search ranks

`search_messages` runs FTS5 keyword search and vector retrieval in parallel,
then merges the two lists with reciprocal-rank fusion (RRF), which is robust
to the two result sets' incomparable score scales. Keyword hits keep their
highlighted snippet; semantic-only hits carry the message body. If the LLM
endpoint or embeddings are unavailable, the vector half is skipped and you get
keyword results only (the degradation is logged to stderr so it is
diagnosable).

## Privacy notes

- The MCP server reads only your local database in `data_dir`; it never
  touches the archive's encrypted `.snapshots`.
- The only network egress is embedding your **query text** via `llm.base_url`
  (for `semantic_search` and the semantic half of `search_messages`). With the
  default local endpoint, nothing leaves the machine.
- What the connected AI assistant does with retrieved messages is governed by
  that assistant, not msgbrowse — connect clients you trust with your message
  history.
