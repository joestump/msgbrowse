// The semantic-index progress card on the Providers page (issue #191): one
// always-present card between the intro copy and the source cards that says,
// honestly, where the vector index stands — off (no embed model configured),
// indexing (live progress bar), behind (counts + an explicit Resume), current,
// or failed (pointing at Settings → Logs).
//
// The card is driven by two inputs: the live LLM settings (is an embed model
// configured at all?) and the background embedding job behind the EmbedIndexer
// seam (embedsvc.Job in serve and desktop; fakes in tests). While a run is in
// flight the card renders a native <progress> bar from the job's in-memory
// counts and self-polls GET /setup/embed/status every 2s — the setup_progress
// pattern — and STOPS polling the moment the fragment renders a non-running
// state. Idle states read two COUNT queries from the store (missing + total
// embeddable); the 2s poll never touches the store.
//
// Egress posture (ADR-0010 / issue #191): nothing here auto-starts embedding.
// The only mutation is POST /setup/embed/resume — the explicit "Resume
// indexing" affordance — and it is gated exactly like every privileged Setup
// POST (checkSetupPOST: same-origin + per-session token + body cap, 403
// before any work).
package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/joestump/msgbrowse/internal/embedsvc"
)

// EmbedIndexer is the seam the serve/desktop shells implement (with
// embedsvc.Job) to run and observe the background embedding job. It mirrors
// the Enabler seam: the web layer renders from value snapshots and triggers
// work through it, never owning the job itself. nil (never wired — e.g. a
// bare test server) renders the informational states but no Resume control.
type EmbedIndexer interface {
	// Status returns a live snapshot of the background embedding job.
	Status() embedsvc.Status
	// Notes returns the bounded run-history lines (counts and timings only —
	// never message content) for the Settings → Logs page.
	Notes() []embedsvc.Note
	// Kick starts the incremental embed pass when an embed model is configured
	// and no run is in flight; otherwise it is a no-op. It reports whether a
	// run was started and never blocks on the run itself.
	Kick() bool
}

// SetEmbedIndexer wires the background embedding job. Call it after NewServer
// and before serving begins — handlers read the field without locking, so
// late wiring would race (the SetEnabler contract).
func (s *Server) SetEmbedIndexer(e EmbedIndexer) { s.embedIndexer = e }

// Embed-card states. Fixed tokens: the template keys its copy on them and the
// CSS keys the state class, so nothing request-derived reaches either.
const (
	// embedStateUnconfigured: no embed model set — semantic search is off.
	embedStateUnconfigured = "unconfigured"
	// embedStateRunning: the background job is embedding right now.
	embedStateRunning = "running"
	// embedStateBehind: idle with messages still lacking embeddings.
	embedStateBehind = "behind"
	// embedStateCurrent: idle with every embeddable message indexed.
	embedStateCurrent = "current"
	// embedStateFailed: the last run stopped on an error (details in Logs).
	embedStateFailed = "failed"
)

// embedCardData drives the embed_progress_card fragment. Every field is a
// server-computed count or fixed token — no request-derived content — so
// html/template escaping is the only encoding needed.
type embedCardData struct {
	// State is one of the embedState* tokens above.
	State string
	// Processed / Total are the RUNNING state's live progress (this run's
	// counts, from the job's memory). Total 0 means the run is still counting —
	// the template renders an indeterminate bar.
	Processed int
	Total     int
	// Missing / Indexed are the idle states' store counts: messages still
	// lacking an embedding for the current model, and messages already indexed
	// (embeddable minus missing).
	Missing int
	Indexed int
	// Available reports whether an EmbedIndexer is wired, so the Resume
	// affordance renders only where a POST could actually start a run.
	Available bool
	// Token is the fresh per-session token armed on the Resume POST
	// (SPEC-0013 §Security — the same gate as every privileged Setup POST).
	Token string
}

// Polling reports whether the fragment should self-poll /setup/embed/status:
// only while a run is in flight. A non-running render carries no hx-trigger,
// which is exactly how the polling stops.
func (d embedCardData) Polling() bool { return d.State == embedStateRunning }

// embedCard computes the card's state. token arms the Resume POST; it is the
// page/fragment render's minted token.
func (s *Server) embedCard(ctx context.Context, token string) embedCardData {
	d := embedCardData{Available: s.embedIndexer != nil, Token: token}

	model := strings.TrimSpace(s.currentLLM().EmbedModel)
	if model == "" {
		d.State = embedStateUnconfigured
		return d
	}

	var jobState embedsvc.State
	if s.embedIndexer != nil {
		st := s.embedIndexer.Status()
		jobState = st.State
		if st.State == embedsvc.StateRunning {
			d.State = embedStateRunning
			d.Processed = st.Processed
			d.Total = st.Total
			return d
		}
	}

	// Idle (never ran / done / cancelled / failed): the store is the truth for
	// how far the index is behind. A count error degrades to the "current"
	// line with zero counts rather than a 500 — the next render retries.
	missing, err := s.store.CountMissingEmbeddings(ctx, model)
	if err != nil {
		s.log.Warn("embed card: could not count missing embeddings", "error", err)
	}
	total, terr := s.store.CountEmbeddable(ctx)
	if terr != nil {
		s.log.Warn("embed card: could not count embeddable messages", "error", terr)
	}
	d.Missing = missing
	d.Indexed = total - missing
	if d.Indexed < 0 {
		d.Indexed = 0
	}

	switch {
	case jobState == embedsvc.StateFailed:
		// The last run stopped on an error. Honest one-liner pointing at
		// Settings → Logs; the Resume affordance offers the retry (stored
		// batches persist, so it picks up where the failed run stopped).
		d.State = embedStateFailed
	case missing > 0:
		d.State = embedStateBehind
	default:
		d.State = embedStateCurrent
	}
	return d
}

// handleEmbedStatus is GET /setup/embed/status: the card fragment the RUNNING
// state polls every 2s. A safe GET (no mutation, no job started) — reading
// counts leaks nothing a same-origin page cannot already see, matching
// /setup/status/{source}. It mints a fresh token so a fragment that lands in
// an idle-and-behind state carries a live Resume control.
func (s *Server) handleEmbedStatus(w http.ResponseWriter, r *http.Request) {
	tok, err := s.setupTokens.mint()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "embed_progress_card", s.embedCard(r.Context(), tok))
}

// handleEmbedResume is POST /setup/embed/resume — the explicit "Resume
// indexing" affordance (and the failed state's retry). It enforces the
// privileged-POST gate FIRST (same-origin + per-session token + body cap; a
// failing request is 403 with NO run started — embedding is network egress),
// then kicks the job and renders the refreshed card. A Kick that reports
// false (unconfigured, already running, no indexer) is not an error: the
// re-rendered card states the truth either way.
func (s *Server) handleEmbedResume(w http.ResponseWriter, r *http.Request) {
	if !s.checkSetupPOST(w, r) {
		return // 403 already written; no run started
	}
	if s.embedIndexer != nil {
		s.embedIndexer.Kick()
	}
	tok, err := s.setupTokens.mint()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "embed_progress_card", s.embedCard(r.Context(), tok))
}

// embedNoteSnapshot reads the embedding job's run-history lines for the Logs
// page, or nothing when no indexer is wired.
func (s *Server) embedNoteSnapshot() []embedsvc.Note {
	if s.embedIndexer == nil {
		return nil
	}
	return s.embedIndexer.Notes()
}
