# SPEC-0016 Design: AI-editorialized journal

- **Capability:** journal
- **Related ADRs:** [ADR-0023](../../../adr/0023-ai-editorialized-journal.md), [ADR-0011](../../../adr/0011-contact-facts-extraction.md), [ADR-0010](../../../adr/0010-security-privacy-posture.md)
- **Related specs:** [SPEC-0005 (contact facts)](../contact-facts/spec.md)

## Architecture

The journal mirrors the facts pipeline, split into two layers. A dedicated
command drives an orchestrator that (1) rebuilds the mechanical rollup from the
store with no network, then (2) — only when digests are enabled — assembles each
eligible day's transcript, calls the LLM once per day, and caches the prose.
Orchestration lives in `internal/journal`; persistence and the day-keyed caches
live in `internal/store`. The web layer reads both caches and never calls the LLM.

```
internal/cli/journal.go ──▶ journal.Run(store, llm.Client, Options)
                              │
internal/journal/journal.go ──┼─ BuildMechanical (no LLM, no egress):
                              │    per UTC day: counts, per-source counts, top senders
                              │    → store.PutJournalDay (upsert, keyed on day)
                              ├─ if digest_enabled and not --dry-run:
                              │    EnumerateEligibleDays (exclude list applied here)
                              │      → days missing a current (model, prompt_version)
                              │      → capped by max_days_per_run (unless --backfill)
                              │    per day (bounded): AssembleTranscript (exclude list)
                              │      → buildPrompt → llm.Chat (~180s timeout) → body
                              │      → store.PutJournalDigest (day, model, prompt_version)
                              └─ Summary (days built, digested, remaining, tokens est.)
internal/web/journal.go ─────▶ store.JournalDaysPage (keyset) + JournalDigest fallback
```

`prompt_version = sha256hex(lower(trim(effective DigestPrompt)))`, computed once
per run and threaded through enumeration and persistence so eligibility and the
stored key can never drift.

## Key design decisions

### Two layers, one command, independent gating

The mechanical rollup and the digest are separate passes of `journal.Run`. The
mechanical pass reads only `messages` and always runs, so a machine with digests
disabled still gets a complete, deterministic journal at zero egress cost. The
digest pass runs only when `journal.digest_enabled` is true and is the sole
`llm.Chat` egress — exactly the posture `facts` established ([ADR-0011](../../../adr/0011-contact-facts-extraction.md),
[ADR-0010](../../../adr/0010-security-privacy-posture.md)). Splitting them keeps
the always-on layer honest (no accidental network) and the opt-in layer auditable
(one call per day, one endpoint).

### UTC day bucketing is the load-bearing correctness rule

A day is `substr(ts,1,10)`, equivalently `date(ts_unix,'unixepoch')`.
`messages.ts` is a wall-clock string `'YYYY-MM-DD HH:MM:SS'` and `messages.ts_unix`
is that string parsed **as UTC** (`internal/signal/message.go`: "local time with
no zone, parsed as UTC purely for a stable ordering key"). Because both encode the
same already-UTC instant, day derivation is a pure slice with **no** timezone
conversion. Introducing `'localtime'` anywhere would double-shift the value and
push messages near midnight into the wrong day — the single highest-consequence
bug this feature can have. The mechanical rollup and the per-day transcript query
use the identical UTC day expression so counts and digests always agree.

### Day-keyed, FK-less caches (schema v11)

`journal_days(day PRIMARY KEY, message_count, conversation_count, source_counts,
top_senders, updated_at)` caches the mechanical rollup; `source_counts` and
`top_senders` are small JSON blobs read whole. `journal_digests(day PRIMARY KEY,
model, prompt_version, body, updated_at)` caches the prose. **Neither table has a
foreign key to `messages`**: `ReplaceConversationMessages` deletes and re-inserts a
conversation's message rows on every re-ingest (rowids change, content does not),
so a CASCADE would wipe every derived journal row on each import — the same reason
embeddings (v3) and `contact_facts` (v4) omit the FK. Both tables are day-keyed,
so the migration's `foreign_key_check` passes trivially, and a stale/missing
`journal_days` row is simply re-derived (it is a cache, not a source of truth).

### Digest cache key and invalidation

`journal_digests` stores the `model` and `prompt_version` that produced each
day's `body`. `prompt_version` is `sha256hex(lower(trim(effective DigestPrompt)))`
— the same normalization `factHash` applies in `internal/store/facts.go`, reused
so the two features hash prompt-shaped text identically. Eligibility is "no row,
or a row whose `(model, prompt_version)` differs from the current effective
values." This means editing `journal.digest_prompt` or switching `llm.chat_model`
invalidates cached digests **without** a wipe: the key no longer matches and the
day re-digests on the next run, updating the row in place (`PutJournalDigest`
upserts on `day`). `--regenerate` remains the explicit "wipe and rebuild all"
escape hatch; a day whose key still matches costs zero LLM calls.

### One digest per day

The digest is a single cross-thread daily summary keyed on `day`, matching
`config.DefaultDigestPrompt` ("summarizing one day"). Keying on `day` alone (not
`(day, conversation)`) is deliberate: per-conversation digests are a future
extension, and the day-keyed schema leaves room to add a companion table later
without disturbing this one.

### Privacy boundary

`journal.exclude_conversations` is applied while enumerating days and assembling
each day's transcript, **before** any message body is gathered, so an excluded
thread's content never reaches the transcript or the LLM — the same
before-any-read boundary `FactConversations` enforces ([ADR-0010](../../../adr/0010-security-privacy-posture.md)).
A day remains eligible when non-excluded conversations have messages that day; the
excluded thread simply contributes nothing. The sole egress is `llm.Chat` to
`llm.base_url`.

### CLI semantics, per-run cap, and an honest dry-run

A default run is incremental: rebuild the mechanical rollup, then digest every day
whose cached digest is absent or stale by `(model, prompt_version)`, capped at
`journal.max_days_per_run` (0 = unbounded) for cron and reporting the remaining
eligible-day count. `--since` sets a day floor; `--backfill` runs the same
eligibility across all history ignoring the cap; `--regenerate` wipes digests then
rebuilds. A longer per-day context timeout (~180s vs. the tight facts default)
absorbs large daily transcripts.

`--dry-run` makes **zero** `Chat` calls: it enumerates the eligible days and
estimates input tokens with a local `len(runes)/4` heuristic. It intentionally
prints **no** dollar figure — there is no tokenizer or price table in the codebase
today, and the OpenAI-compatible response's `usage` object is not decoded, so any
cost number would be fabricated. A real estimate is stated future work (it would
need a price-config knob plus `usage` decoding), and the dry-run output says so
rather than implying a precision it does not have.

### Web surface

`GET /journal` lists days newest-first via `store.JournalDaysPage` (keyset
pagination over `day`), rendering each day's cached digest when present and
falling back to the mechanical rollup (counts + top senders) otherwise, with an
empty state when no days exist. The page reads only the two caches and never
triggers an LLM call. The home page gains a fourth quick-link icon tile pointing
at `/journal`, alongside the existing tiles.

## Testing

- `internal/store/journal_test.go`: mechanical rollup correctness and UTC day
  bucketing (incl. a near-midnight message staying on its UTC day, and a
  `'localtime'` regression guard), `PutJournalDay` upsert idempotency, digest
  upsert + `(model, prompt_version)` round-trip, eligibility after prompt/model
  change, keyset day paging, re-ingest leaves derived rows intact (no FK cascade).
- `internal/journal/run_test.go`: end-to-end run with a fake `llm.Client` —
  mechanical build with digests disabled makes no calls; incremental digest skips
  current days; `--regenerate` rebuilds; `--backfill` ignores the cap;
  `max_days_per_run` caps and reports the remainder; exclude list keeps a thread's
  content out of the assembled transcript; `--dry-run` makes zero calls and prints
  a token estimate with no dollar figure.
- `internal/web/journal_web_test.go`: `/journal` renders days newest-first with
  digest-or-mechanical fallback, paginates, shows the empty state, and issues no
  LLM call; the home page renders the `/journal` quick-link tile.
