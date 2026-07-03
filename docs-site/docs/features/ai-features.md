---
title: AI features
sidebar_label: AI features
sidebar_position: 5
---

# AI features

All of msgbrowse's AI features talk to exactly **one** endpoint: the
OpenAI-compatible URL you configure as `llm.base_url`. By default that is a
local LiteLLM proxy (`http://127.0.0.1:4000/v1`) routing to a local model, so
out of the box no message content ever leaves your machine. There is no
telemetry and no other outbound connection — the LLM endpoint is the only
egress in the entire product.

:::warning The one place data can leave the box
If you point `llm.base_url` at a hosted provider, the AI features below send
message content (and, for the upcoming journal's opt-in captioning, media
bytes) off-device. Keeping the default local route keeps everything on the
machine. See [SECURITY.md](https://github.com/joestump/msgbrowse/blob/main/SECURITY.md)
for the exact data-sent-to-the-LLM boundary, feature by feature.
:::

## LLM configuration

| Key | Env | Default | Notes |
| --- | --- | --- | --- |
| `llm.base_url` | `MSGBROWSE_LLM_BASE_URL` | `http://127.0.0.1:4000/v1` | The only network egress. |
| `llm.api_key` | `MSGBROWSE_LLM_API_KEY` | — | Env/secret only; never commit it. |
| `llm.chat_model` | `MSGBROWSE_LLM_CHAT_MODEL` | `local-chat` | Used for facts and digests. |
| `llm.embed_model` | `MSGBROWSE_LLM_EMBED_MODEL` | `local-embed` | Used for embeddings. |
| `llm.max_concurrency` | `MSGBROWSE_LLM_MAX_CONCURRENCY` | `4` | Parallel LLM calls. |
| `llm.timeout` | `MSGBROWSE_LLM_TIMEOUT` | `60s` | Per-request timeout. |

## The privacy guard: `journal.exclude_conversations`

`journal.exclude_conversations` is a denylist of conversation names whose
content is **never sent to any LLM, for any feature** — facts, embeddings, and
the upcoming journal all honor it. Use it for conversations you want browsable
and keyword-searchable but off-limits to AI processing:

```yaml
journal:
  exclude_conversations:
    - "Therapist"
    - "Family Group"
```

## Contact facts

`msgbrowse facts` sends each contact's messages to your configured chat model
and stores the atomic facts it extracts — things like "Has a dog named
Biscuit" — each with a citation back to the exact source message:

```sh
msgbrowse --data-dir ./data facts
```

- **Cited.** Every fact links to the message it came from; the conversation
  page renders the facts panel with jump-to-context links and an explicit
  "AI-generated — may be incomplete or wrong" disclaimer.
- **Incremental.** A per-conversation cursor means re-running after an import
  only analyzes new messages, and facts are deduplicated per contact, so
  reprocessing never creates duplicates.
- **Flags:** `--reset` wipes stored facts and cursors and rebuilds;
  `--batch-size` (default 60) sets messages per extraction call;
  `--concurrency` (default 4) sets parallel conversations; `--conversation`
  limits the run to a single conversation ID.

## Embeddings and semantic search

`msgbrowse embed` computes a vector for each message so search can match by
meaning, not just keywords:

```sh
msgbrowse --data-dir ./data embed
```

- **Incremental.** Only messages without an embedding for the current
  `llm.embed_model` are processed; re-run after each import.
- `--prune` removes embeddings whose message no longer exists.
- Vectors are stored via the backend selected by `vector_backend`:
  `sqlite-vec` (the default — everything stays in your data directory) or
  `qdrant`.
- What it sends: message text goes to `llm.base_url` to be embedded. With the
  default local endpoint, that text never leaves the machine.

Semantic search is consumed through the [MCP server](./mcp-server.md): the
`semantic_search` tool does pure vector retrieval, and `search_messages`
fuses keyword and vector results into a hybrid ranking. Browsing and keyword
search work fine without ever running `embed`.

## Upcoming: the AI-editorialized journal

:::note Upcoming feature
The journal is under active construction and **not functional yet** — the
`msgbrowse journal` command exists (with `--since`, `--backfill`,
`--regenerate`, and `--dry-run` flags) but currently returns "not implemented."
This section describes where it is headed.
:::

The headline feature in progress is a Daylio-style daily journal the LLM
writes from your chats: a mechanical day-by-day Markdown journal, plus an
optional LLM digest pass per day (summary, key people, themes, notable
decisions and links). Its configuration keys are already in place:

| Key | Default | Notes |
| --- | --- | --- |
| `journal.digest_enabled` | `true` | Turn the LLM digest pass on or off; the mechanical journal is always written. |
| `journal.digest_prompt` | built-in | The digest instruction prompt. Changing it invalidates the digest cache. |
| `journal.exclude_conversations` | `[]` | The privacy denylist described above. |
| `journal.max_days_per_run` | `0` (unbounded) | Cap how many days one digest run processes. |

Optional image captioning and audio transcription for the journal will be
opt-in, because they send raw media bytes to the LLM endpoint — a much
heavier egress than text if you route to a hosted model.
