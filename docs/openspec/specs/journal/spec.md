# SPEC-0016: AI-editorialized journal

- **Status:** Accepted
- **Date:** 2026-07-11
- **Capability:** journal
- **Source packages:** `internal/journal`, `internal/store` (`journal.go`, `schema.go` v11), `internal/cli` (`journal.go`), `internal/web` (`journal.go`, `templates/journal.html`, `templates/home.html`)
- **Related ADRs:** [ADR-0023 (AI-editorialized journal)](../../../adr/0023-ai-editorialized-journal.md), [ADR-0011 (contact facts extraction)](../../../adr/0011-contact-facts-extraction.md), [ADR-0010 (security & privacy posture)](../../../adr/0010-security-privacy-posture.md)
- **Related specs:** [SPEC-0005 (contact facts)](../contact-facts/spec.md) — the sibling extraction feature

## Overview

msgbrowse builds a day-by-day journal over the local archive in two layers. The
**mechanical** layer is a deterministic per-day rollup (counts and top senders)
derived entirely from `messages` with no LLM and no network egress; it is always
built. The **digest** layer is one prose summary per day written by the
configured chat model, cached in SQLite and keyed by `(day, model,
prompt_version)`; it is the only network egress and is gated on
`journal.digest_enabled`. Both layers bucket messages by **UTC** calendar day,
honor `journal.exclude_conversations` before any content is assembled, and are
re-ingest-safe (day-keyed, no foreign key to `messages`).

## Requirements

### REQ-0016-001: Mechanical day journal, deterministic and egress-free

The mechanical journal MUST be a per-day rollup — message count, conversation
count, per-source counts, and top senders — derived solely from the local
`messages` table, with NO LLM call and NO network egress. It MUST be built on
every `msgbrowse journal` run regardless of `journal.digest_enabled`. Rebuilding
it MUST be idempotent: a re-run over unchanged messages MUST produce the same
`journal_days` rows (an upsert keyed on `day`), never duplicates.

#### Scenario: Mechanical build never egresses
- **Given** an imported archive and `journal.digest_enabled = false`
- **When** the user runs `msgbrowse journal`
- **Then** per-day `journal_days` rows are written from `messages` alone, no call is made to `llm.base_url`, and re-running produces identical rows.

### REQ-0016-002: UTC day bucketing

A day MUST be derived as `substr(ts,1,10)` (equivalently
`date(ts_unix,'unixepoch')`). Because `messages.ts_unix` is the wall-clock string
parsed **as UTC**, day bucketing MUST NOT apply any timezone conversion; using
`'localtime'` (which would double-shift the already-UTC value and misfile
messages across midnight) is prohibited. The mechanical rollup and the transcript
assembled for a digest MUST use the same UTC day definition.

#### Scenario: A late-night message stays on its own UTC day
- **Given** a message with `ts = '2026-07-01 23:30:00'` (`ts_unix` = that instant as UTC)
- **When** the journal is built
- **Then** it is counted under day `2026-07-01`, and no `'localtime'` conversion shifts it into an adjacent day.

### REQ-0016-003: Opt-in digest as the single egress

The LLM digest MUST be produced only when `journal.digest_enabled` is true, and
each digest MUST be the only network egress — one `llm.Chat` call per day to
`llm.base_url` using `llm.chat_model`. Import and `serve` MUST NOT trigger digest
generation. A per-day digest call MUST use a longer per-call context timeout
(~180s) than the facts default, because a full day's transcript can be large.

#### Scenario: Digest is explicit and gated
- **Given** `journal.digest_enabled = true` and days with no cached digest
- **When** the user runs `msgbrowse journal`
- **Then** each eligible day is summarized via `llm.base_url`; running `signal-import` or `serve` alone never calls the LLM, and setting `digest_enabled = false` produces the mechanical journal with zero LLM calls.

### REQ-0016-004: Digest cache keyed by day, model, and prompt version

Each digest MUST be cached in `journal_digests` (`PRIMARY KEY day`) alongside the
`model` and `prompt_version` that produced it, where `prompt_version =
sha256hex(lower(trim(effective DigestPrompt)))` (the same normalization recipe as
`factHash`). A day whose cached `(model, prompt_version)` matches the current
effective values MUST be skipped with no LLM call. Editing `journal.digest_prompt`
or switching `llm.chat_model` MUST make previously-digested days eligible again on
the next run. There MUST be no foreign key from `journal_digests` (or
`journal_days`) to `messages`.

#### Scenario: Prompt change invalidates the cache
- **Given** a day already digested under prompt version `P1` and model `M`
- **When** `journal.digest_prompt` is edited (yielding version `P2`) and `msgbrowse journal` runs again
- **Then** that day is re-digested and its row is updated to `(M, P2)`; with the prompt and model unchanged instead, the day is skipped with no LLM call.

### REQ-0016-005: One digest per day

The digest MUST be a single cross-thread summary per calendar day (matching
`config.DefaultDigestPrompt`, "summarizing one day"), keyed on `day` alone.
Per-conversation digests are OUT OF SCOPE for this capability.

#### Scenario: Multi-thread day yields one digest
- **Given** a day with messages across several conversations
- **When** the digest for that day is generated
- **Then** exactly one `journal_digests` row is written for that day, summarizing the day as a whole.

### REQ-0016-006: Honor the exclude list before assembly

`journal.exclude_conversations` MUST be applied during day enumeration and
transcript assembly, **before** any message content is gathered, so an excluded
conversation's content never reaches the assembled transcript or the LLM. This
MUST NOT affect a day's inclusion when other, non-excluded conversations have
messages that day.

#### Scenario: Excluded thread is never sent
- **Given** a conversation named in `journal.exclude_conversations` with messages on a given day
- **When** that day's digest is generated
- **Then** the excluded conversation's content is absent from the transcript sent to `llm.base_url`, while non-excluded threads from the same day are summarized normally.

### REQ-0016-007: CLI flags and run semantics

`msgbrowse journal` MUST expose `--since YYYY-MM-DD`, `--backfill`,
`--regenerate`, and `--dry-run`. A default run MUST be **incremental**: it
digests every day whose cached digest is absent or stale by `(model,
prompt_version)`. `--backfill` MUST apply the same eligibility across all history
with no day cap. `--regenerate` MUST wipe all cached digests and rebuild them.
`--since` MUST set a day floor (process only days on or after the given date).

#### Scenario: Incremental default skips current days
- **Given** some days already digested under the current model and prompt and some not
- **When** `msgbrowse journal` runs with no flags
- **Then** only days lacking a current digest are sent to the LLM; already-current days are skipped.

#### Scenario: Regenerate rebuilds everything
- **Given** a fully-digested history
- **When** `msgbrowse journal --regenerate` runs
- **Then** all cached digests are wiped and re-generated for every eligible day.

### REQ-0016-008: Per-run day cap with remaining count

An incremental run MUST cap the number of days it digests at
`journal.max_days_per_run` (0 = unbounded) so a scheduled run has a bounded cost,
and MUST report how many eligible days remain after the cap so the operator can
schedule follow-up runs. `--backfill` MUST ignore the cap.

#### Scenario: Cap bounds a cron run and reports the remainder
- **Given** 50 days eligible for a digest and `journal.max_days_per_run = 20`
- **When** `msgbrowse journal` runs
- **Then** 20 days are digested and the run reports 30 days remaining; a subsequent run digests the next batch.

### REQ-0016-009: Dry-run makes zero LLM calls

`--dry-run` MUST make NO `llm.Chat` calls. It MUST enumerate the days that lack a
current digest and estimate input tokens with a local `len(runes)/4` heuristic.
It MUST NOT print a dollar/cost figure — the codebase has no tokenizer or price
table and does not decode the provider's `usage` object, so a real cost estimate
is explicitly future work.

#### Scenario: Dry-run reports days and a token estimate only
- **Given** several days lacking a current digest
- **When** `msgbrowse journal --dry-run` runs
- **Then** it prints the eligible-day count and an approximate input-token total (`runes/4`), makes no network call, and prints no dollar amount.

### REQ-0016-010: Journal web page and home quick-link

`GET /journal` MUST list days newest-first, showing each day's digest when one is
cached and falling back to the mechanical rollup when it is not, with keyset
pagination over days and an empty state when no days exist. The home page MUST
offer a fourth quick-link icon tile linking to `/journal`. Rendering the page
MUST NOT trigger any LLM call.

#### Scenario: Days render newest-first with fallback
- **Given** a day with a cached digest and an earlier day with only a mechanical rollup
- **When** the user opens `/journal`
- **Then** the digested day appears first showing its prose, the earlier day shows its counts and top senders, older days page in via keyset pagination, and no LLM call is made.

#### Scenario: Empty state
- **Given** an archive with no imported messages
- **When** the user opens `/journal`
- **Then** the page renders an empty state rather than an error or a blank list.
