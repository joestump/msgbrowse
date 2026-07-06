// The privileged Setup Refresh flow: POST /setup/refresh re-runs export +
// incremental import for one already-Enabled source. Refresh is the SPEC-0013
// REQ "Refresh": it re-runs the SAME export→adopt→import pipeline as Enable so
// only the delta lands (the importer is incremental + idempotent), reusing the
// same background-job, progress, cancellation, and concurrency machinery —
// nothing here reimplements the pipeline. (The former all-sources fan-out,
// POST /setup/refresh-all, was removed per issue #194 — per-source Refresh is
// the whole surface, which also obsoleted #146's per-source-progress seeding.)
//
// The route carries the IDENTICAL privileged-POST gate as /setup/enable —
// same-origin + per-session token + MaxBytesReader body cap via checkSetupPOST,
// unchanged (SPEC-0013 §Security endpoint table: "/setup/refresh … Same as
// /setup/enable … Same-origin required"). A failing gate is rejected 403 with NO
// job started; an unknown source is a 400. The source is read from the fixed
// enum, never a client path.
//
// Governing: ADR-0020, SPEC-0013 REQ "Refresh" (delta-only), REQ "Concurrency
// Safety" (one job per source; the Runner's guard rejects a duplicate
// same-source job), §Security "Same-origin protection for privileged POSTs".
package web

import (
	"errors"
	"net/http"

	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/source"
)

// handleSetupRefresh is POST /setup/refresh. It enforces the privileged-POST gate
// FIRST — a failing request is rejected 403 with NO job started — then starts the
// background refresh job for the fixed-enum source and renders the same progress
// fragment Enable uses, so an Enabled card's Refresh shares the aria-live surface
// and the Done sidebar-refresh with Enable. The source is read from a fixed enum
// (never a client path); an unknown source is a 400.
func (s *Server) handleSetupRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.checkSetupPOST(w, r) {
		return // 403 already written; no job started
	}

	src := r.PostFormValue("source")
	if !source.IsKnown(src) {
		http.Error(w, "unknown source", http.StatusBadRequest)
		return
	}

	// Refresh runs the exporters too, so the importer/replica guard applies
	// exactly as on Enable (#158; SPEC-0014 REQ "Importer and Replica Roles").
	if s.renderImporterConflict(w, r, src) {
		return
	}

	if s.enabler == nil {
		// No orchestrator wired (browser mode with no resolvable tools): render the
		// "unavailable" affordance rather than 500ing, exactly as Enable does.
		s.renderEnableUnavailable(w, r, src)
		return
	}

	prog, err := s.enabler.Refresh(src)
	if err != nil && !errors.Is(err, onboard.ErrJobInProgress) {
		// A start-time error other than "already running" (runner shutting down,
		// unknown source): surface it as a failed progress fragment, not a bare 500.
		s.renderProgressError(w, r, src, err)
		return
	}
	// ErrJobInProgress coalesces the duplicate click onto the live job — the
	// existing job's progress is returned and rendered.
	s.renderProgress(w, r, src, prog)
}
