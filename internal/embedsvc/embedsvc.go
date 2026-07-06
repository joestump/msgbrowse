// Package embedsvc runs the incremental embedding pass (internal/embed) as a
// supervised background job — the desktop/serve counterpart of `msgbrowse
// embed` (issue #191). It is a SINGLETON job: unlike the per-source onboard
// Runner, the embed pass covers the whole store, so at most one run is ever in
// flight and a second trigger while one is running is a deliberate no-op.
//
// Egress posture (ADR-0010): embedding sends message text to the configured
// LLM endpoint, so the job NEVER starts itself at app launch. Kick is called
// only at user-attributable moments — after a successful import/refresh, after
// a Settings → LLM save, or from the explicit "Resume indexing" affordance on
// the Providers page — and only when an embed model is configured.
//
// The job reads the embed model and client through the process's live
// llm.Holder seam, so a Settings → LLM save applies to the very next run with
// no restart. Progress is counts-only (embedded N of M, timings, error text) —
// never message content — and flows into the Settings → Logs surface through
// the bounded Notes ring, mirroring the onboarding jobs' JobLog machinery.
//
// Shutdown cancels the run context; internal/embed persists each stored batch,
// so a cancelled run loses nothing — the next Kick simply picks up the
// remainder (the pass is incremental by design).
package embedsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/joestump/msgbrowse/internal/embed"
	"github.com/joestump/msgbrowse/internal/llm"
	"github.com/joestump/msgbrowse/internal/store"
)

// State is the job's lifecycle token. It is a plain string so templates can
// key on it directly.
type State string

const (
	// StateIdle: no run has started this session.
	StateIdle State = "idle"
	// StateRunning: a run is in flight (the singleton guard rejects a second).
	StateRunning State = "running"
	// StateDone: the last run completed (possibly embedding nothing — already
	// up to date).
	StateDone State = "done"
	// StateFailed: the last run stopped on an error; stored batches persist,
	// so a later Kick resumes from the remainder.
	StateFailed State = "failed"
	// StateCancelled: the last run was stopped by shutdown/cancellation with
	// its progress persisted — resuming later picks up the remainder.
	StateCancelled State = "cancelled"
)

// Status is a value snapshot of the job, safe to hand to the UI without
// holding the job's lock. Counts only — never message content.
type Status struct {
	// State is the lifecycle token above.
	State State
	// Model is the embed model the current/last run used.
	Model string
	// Processed / Total are this run's progress: Processed messages embedded of
	// the Total the run set out to embed. Total is 0 until the run has counted
	// its work (render an indeterminate bar).
	Processed int
	Total     int
	// Err is the terminal error when State == StateFailed; nil otherwise.
	Err error
	// StartedAt / UpdatedAt bound the run's lifetime for the UI.
	StartedAt time.Time
	UpdatedAt time.Time
	// DurationMS is the last completed run's wall time.
	DurationMS int64
}

// Running reports whether a run is in flight.
func (s Status) Running() bool { return s.State == StateRunning }

// ErrText returns the terminal error string, or "" — the template-safe
// accessor (the onboard.Progress convention).
func (s Status) ErrText() string {
	if s.Err == nil {
		return ""
	}
	return s.Err.Error()
}

// Note is one bounded log line for the Settings → Logs viewer — the embedding
// job's own stream beside the onboarding JobLogs and the sync/shell notes. It
// mirrors devsync.Note so the Logs template renders it identically. Counts and
// timings only, never message content.
type Note struct {
	// Time is when the line was recorded.
	Time time.Time
	// Level is NoteInfo or NoteError.
	Level string
	// Message is the human-readable line (counts, timings, error text).
	Message string
}

// Note levels (the devsync.Note / web.ShellNote convention).
const (
	NoteInfo  = "info"
	NoteError = "error"
)

// IsError reports whether the note is an error, for the template's badge.
func (n Note) IsError() bool { return n.Level == NoteError }

// Clock renders the note's time-of-day for the Logs page (the ShellNote
// convention — runs are read within one app session, so the date is noise).
func (n Note) Clock() string { return n.Time.Format("15:04:05") }

// notesCap bounds the notes ring: a handful of runs per session at 2–3 lines
// each — 64 keeps a full session's history while capping memory.
const notesCap = 64

// Job is the singleton background embedding job. Construct it with New; Kick
// starts a run, Status/Notes observe it, Shutdown tears it down. All methods
// are safe for concurrent use.
type Job struct {
	st     *store.Store
	client llm.Client
	model  func() string
	log    *slog.Logger

	baseCtx context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup

	mu     sync.Mutex
	status Status
	notes  []Note
	now    func() time.Time // injectable clock for tests
}

// New builds the job over the process's store and live LLM seam. client is
// normally the shared *llm.Holder and model its EmbedModel getter, so every
// run sees the CURRENT endpoint and model — a Settings → LLM save needs no
// rewiring here. A nil log uses slog.Default().
func New(st *store.Store, client llm.Client, model func() string, log *slog.Logger) *Job {
	if log == nil {
		log = slog.Default()
	}
	base, stop := context.WithCancel(context.Background())
	return &Job{
		st:      st,
		client:  client,
		model:   model,
		log:     log,
		baseCtx: base,
		stop:    stop,
		status:  Status{State: StateIdle},
		now:     time.Now,
	}
}

// Kick starts the incremental embed pass in the background and reports whether
// a run was started. It is a no-op (false) when no embed model is configured
// (semantic search is off — no egress), when a run is already in flight (the
// singleton guard), or when the job is shutting down. It never blocks on the
// run itself; callers poll Status.
//
// A Kick with nothing left to embed still runs — the pass finds zero missing
// messages, makes NO network calls, and completes as Done immediately — so
// callers need no "is the index behind?" pre-check.
func (j *Job) Kick() bool {
	model := strings.TrimSpace(j.model())
	if model == "" {
		return false
	}

	j.mu.Lock()
	if j.status.State == StateRunning {
		j.mu.Unlock()
		return false
	}
	select {
	case <-j.baseCtx.Done():
		j.mu.Unlock()
		return false
	default:
	}
	now := j.now()
	j.status = Status{State: StateRunning, Model: model, StartedAt: now, UpdatedAt: now}
	runCtx, cancel := context.WithCancel(j.baseCtx)
	j.wg.Add(1)
	j.mu.Unlock()

	go func() {
		defer j.wg.Done()
		defer cancel()
		j.run(runCtx, model)
	}()
	return true
}

// Status returns a value snapshot of the job.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// Notes returns the bounded run-history lines, oldest first, for the Logs
// page. The returned slice is a copy.
func (j *Job) Notes() []Note {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Note(nil), j.notes...)
}

// Shutdown cancels any in-flight run and blocks until its goroutine has
// exited. Stored batches persist (internal/embed writes each batch before
// moving on), so nothing is lost — a later Kick resumes from the remainder.
// Idempotent; part of the app's graceful-shutdown path beside the onboard
// Runner's Shutdown.
func (j *Job) Shutdown() {
	j.stop()
	j.wg.Wait()
}

// run executes one embed pass and records its terminal state. It never panics
// out to the goroutine; every exit publishes a terminal state.
func (j *Job) run(ctx context.Context, model string) {
	start := j.now()
	j.note(NoteInfo, fmt.Sprintf("embedding run started (model %s)", model))
	sum, err := embed.Run(ctx, j.st, j.client, embed.Options{
		EmbedModel: model,
		Logger:     j.log,
		Progress:   j.setProgress,
	})
	dur := j.now().Sub(start)

	switch {
	case err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil):
		// Cancellation (shutdown) is a clean stop, not a failure: every stored
		// batch persists, so the next Kick resumes from the remainder.
		j.finish(StateCancelled, sum.Embedded, nil, dur)
		j.note(NoteInfo, fmt.Sprintf("embedding run stopped for shutdown — %d embedded and saved; the next run resumes the remainder", sum.Embedded))
	case err != nil:
		j.finish(StateFailed, sum.Embedded, err, dur)
		// Provider/store error text only — never message content.
		j.note(NoteError, fmt.Sprintf("embedding run failed after %d embedded: %v", sum.Embedded, err))
		j.log.Warn("embedding run failed", "embedded", sum.Embedded, "error", err)
	default:
		j.finish(StateDone, sum.Embedded, nil, dur)
		if sum.Embedded == 0 {
			j.note(NoteInfo, "semantic index already up to date — nothing to embed")
		} else {
			j.note(NoteInfo, fmt.Sprintf("embedded %d messages in %d batches (%.1fs)", sum.Embedded, sum.Batches, dur.Seconds()))
		}
	}
}

// setProgress is the embed.Options.Progress hook: it publishes batch progress
// under the lock so Status snapshots are never torn.
func (j *Job) setProgress(processed, total int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status.Processed = processed
	j.status.Total = total
	j.status.UpdatedAt = j.now()
}

// finish records the terminal state for the current run.
func (j *Job) finish(state State, processed int, err error, dur time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status.State = state
	j.status.Processed = processed
	j.status.Err = err
	j.status.UpdatedAt = j.now()
	j.status.DurationMS = dur.Milliseconds()
}

// note appends one bounded log line (dropping the oldest beyond notesCap).
func (j *Job) note(level, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.notes = append(j.notes, Note{Time: j.now(), Level: level, Message: msg})
	if len(j.notes) > notesCap {
		j.notes = j.notes[len(j.notes)-notesCap:]
	}
}
