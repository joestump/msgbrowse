# ADR-0004: MCP SDK choice and RAG retrieval design

- **Status:** Accepted
- **Date:** 2026-06-27

## Context

msgbrowse exposes an MCP server so Claude (Desktop / Code) can answer
natural-language questions over the archive (brief §4). Two decisions needed
fixing: which Go MCP SDK to depend on, and how the retrieval tools should shape
their results so answers are citation-faithful.

## Decision

### SDK: the official `github.com/modelcontextprotocol/go-sdk`

We use the official Go MCP SDK (v1.x, stable) rather than `mark3labs/mcp-go`.

- It is the canonical, spec-tracking implementation maintained alongside the
  protocol; its typed `AddTool[In, Out]` infers JSON Schema from Go structs and
  validates inputs/outputs automatically, which keeps our tool definitions
  declarative and self-documenting.
- It provides both stdio and streamable-HTTP transports out of the box, matching
  the `mcp` / `mcp --http` surface in the brief.
- `NewInMemoryTransports` lets us integration-test the real client↔server round
  trip with no process or socket, so the tools are tested as a client sees them.

Cost: the SDK requires a recent Go toolchain (it bumped our `go` directive to
1.25). That is acceptable — the brief targets Go 1.23+, the Docker build uses the
latest Go, and this is a single-maintainer project.

### Retrieval design: hybrid + citation-faithful, store-backed

- **`search_messages` is hybrid**: it runs FTS5 keyword search and (best-effort)
  vector search, then fuses them with **reciprocal-rank fusion** (RRF,
  `score = Σ 1/(60 + rank)`). RRF is used because the two lists' native scores
  (bm25 vs cosine) are not comparable; rank fusion is scale-free and robust. If
  the LLM endpoint or embeddings are unavailable, it degrades to keyword-only
  rather than failing — the keyword half always works offline.
- **Every result carries exact provenance**: `message_id`, stable `hash`,
  `conversation`, `source`, `sender`, and `timestamp`. The consuming model can
  cite precisely, and a human can jump to the message in the web UI
  (`/c/{id}/at/{message_id}`). The server never returns a passage without its
  coordinates.
- **The MCP layer is a thin adapter over the store.** Tools call the same
  `store` methods the web UI uses (`SearchMessages`, `SemanticSearch`,
  `ConversationTranscript`, `ListAttachments`, `ListLinks`, `GetContext`), so
  keyword/semantic/media behavior cannot drift between the UI and the model.
- **Read-only, minimal egress.** The server never mutates the store or archive.
  Its only network call is embedding the query for semantic search, via the same
  `llm.Client` (and thus the same local-by-default endpoint) as the rest of
  msgbrowse.

### Journal tools deferred

`get_journal_day` and `on_this_day` (brief §4) are deferred to Slice 6, where
journal generation actually produces the entries those tools read. Adding them
now would mean stubbing against data that does not exist yet.

## Consequences

- Tool results are uniform and self-citing; RAG answers built on them can always
  be traced to source messages.
- A future sqlite-vec backend (ADR-0002) changes only `store.SemanticSearch`;
  the MCP tools are unaffected.
- The `go` directive floor is 1.25. Documented in the README build prerequisites.
- The journal tools arrive with the journal (Slice 6), keeping each slice's
  tools backed by real data.

## References

- [ADR-0002: vector backend](0002-vector-backend.md)
- [ADR-0003: dual-source archive](0003-dual-source-archive.md)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
