# 2. Vector backend: sqlite-vec loadable extension, Go brute-force fallback

- Status: accepted
- Date: 2026-06-27

## Context and Problem Statement

Semantic search and RAG (Slice 5) need a vector index over message-chunk
embeddings. The spec defaults to `sqlite-vec` (vec0 virtual table) to keep
everything in one SQLite file with no extra service, and allows a Qdrant sidecar
if clearly better. Given the cgo `mattn` driver ([ADR 0001](0001-sqlite-driver-mattn-cgo.md)),
how do we get vectors into that one file?

## Considered Options

- **`sqlite-vec` as a runtime-loadable extension** loaded into each mattn
  connection (`vec0.so` baked into the container image). Keeps one file, matches
  the spec.
- **Embedded brute-force cosine in Go** — store embeddings as BLOBs in a normal
  table, rank in Go. No extension, trivially robust, one file.
- **Qdrant sidecar** — a dedicated vector service.

## Decision Outcome

Chosen: **`sqlite-vec` as a loadable extension, with the Go brute-force index as
an automatic fallback** when the extension is unavailable (e.g. local
`make build` on a platform without the prebuilt `.so`/`.dylib`).

The owner explicitly chose to pursue sqlite-vec. Loading it as an extension
(rather than statically compiling it, which fights mattn's vendored amalgamation)
keeps the build simple and the data in one file. The brute-force fallback
guarantees semantic search still works everywhere; at single-user personal-archive
scale (hundreds of thousands of chunks) brute-force cosine is fast enough.

Qdrant is rejected as the default: it adds a service, breaks the "one file"
property, and is unnecessary at this scale.

### Consequences

- The container bakes in a `vec0` extension for its platform; the data layer
  detects whether the extension loaded and selects the vec0 path or the Go
  fallback transparently.
- A `vector_backend` config key (`sqlite-vec` | `qdrant`) leaves the door open
  for a future Qdrant implementation without further schema churn.
- Revisit if the archive grows large enough that brute-force latency is felt on
  hosts lacking the extension.
