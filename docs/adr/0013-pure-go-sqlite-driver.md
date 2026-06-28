# ADR-0013: Pure-Go SQLite driver (modernc.org/sqlite) — toolchain-free `go install`

- **Status:** Accepted
- **Date:** 2026-06-28
- **Relates to:** [ADR-0001](0001-sqlite-driver-mattn-cgo.md) (the original cgo driver choice, superseded by this one), [ADR-0002](0002-vector-backend.md) (vectors are brute-force Go, so sqlite-vec is no longer pursued)

## Context

[ADR-0001](0001-sqlite-driver-mattn-cgo.md) chose the cgo `mattn/go-sqlite3`
driver, built with `-tags sqlite_fts5` and `CGO_ENABLED=1`. The *deciding* reason
recorded there was a viable path to in-process vectors via `sqlite-vec` (a C
loadable extension), which the pure-Go drivers cannot load. cgo carried a cost:

- The build needs a C toolchain (gcc), so `go install …@latest` does **not** work
  on a bare Go install — the CI even had a "Verify toolchain" step asserting gcc.
- Every `go build`/`test`/`vet` invocation has to carry `-tags sqlite_fts5` or
  FTS5 (keyword search) silently breaks; a `build_fts5_required.go` tripwire
  existed only to fail the build when someone forgot the tag.
- The container can't be `scratch`/static-distroless; it has to ship glibc, and
  the binary links dynamically.

That deciding reason is now moot. [ADR-0002](0002-vector-backend.md) settled on
**brute-force cosine in pure Go** for vectors (no extension required), so the one
thing that needed a cgo driver — loading `sqlite-vec` — is no longer on the
roadmap. The remaining requirement from the data layer is just **FTS5**, which
the pure-Go drivers provide built in.

## Decision

Switch the SQLite driver to **`modernc.org/sqlite`** (pure Go, FTS5 compiled in),
dropping cgo and the build tag entirely.

- `internal/store/store.go` and `internal/store/migrations_test.go` import
  `modernc.org/sqlite` and `sql.Open("sqlite", dsn)`; the mattn import and the
  `sqlite_fts5` tag are gone. `build_fts5_required.go` (the tag tripwire) is
  deleted — FTS5 is unconditional now.
- FTS5 was **verified working** under modernc: `CGO_ENABLED=0 go test ./...`
  passes with no build tag, including the keyword-search tests in
  `internal/store` and `internal/web`.
- Build config is pinned cgo-off: the Makefile and CI set `CGO_ENABLED=0` and
  carry no tag; the Dockerfile builds a fully static binary on a
  `gcr.io/distroless/static-debian12:nonroot` base.

`go install github.com/joestump/msgbrowse/cmd/msgbrowse@latest` becomes the
**primary** install path — it needs only a Go toolchain, no C compiler, no tag.
**Docker remains a fully supported, optional path** (the owner's explicit
decision): the image is now smaller and static, but `make up` and the compose
workflow are unchanged for users who prefer containers.

## Consequences

- **Good:** `go install` works toolchain-free; the build is reproducible without
  gcc; the container is static and smaller (`static-debian12`, no glibc); no more
  build-tag footgun and no tripwire file to maintain; CI loses the gcc step.
- **Good:** One driver, one DSN, FTS5 always on — the keyword-search path can no
  longer be silently disabled by a missing tag.
- **Trade-off:** `modernc.org/sqlite` is a Go transpilation of SQLite, so it
  cannot load C loadable extensions (e.g. `sqlite-vec`). That door — left open by
  ADR-0001/0002 — is now closed. It is an acceptable loss because vectors are
  brute-force Go ([ADR-0002](0002-vector-backend.md)) and fast enough at
  single-user personal-archive scale.
- **Trade-off:** Pure-Go SQLite can be modestly slower than the cgo amalgamation
  on heavy workloads. At this scale it is not felt; revisit only if the archive
  grows large enough to make query latency noticeable.

This ADR **supersedes** the driver/cgo choice in
[ADR-0001](0001-sqlite-driver-mattn-cgo.md). The FTS5 requirement it established
still holds; only the driver that satisfies it has changed.
