package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
)

// The /setup/refresh gate tests MIRROR the /setup/enable tests (enable_test.go):
// the same checkSetupPOST guard protects both, so the security contract must hold
// identically — 403 on cross-origin / missing / invalid token with NO job
// started, and 400 on an unknown source (SPEC-0013 §Security endpoint table:
// "/setup/refresh … Same as /setup/enable").

// TestRefreshCrossOriginRejected: a cross-origin POST /setup/refresh is rejected
// 403 and starts NO refresh job — even with a valid token, the origin check alone
// must reject.
func TestRefreshCrossOriginRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/refresh", "http://evil.example", tok, source.Signal)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin refresh status = %d, want 403", rec.Code)
	}
	if fe.refreshCount() != 0 {
		t.Fatalf("cross-origin refresh started %d jobs, want 0", fe.refreshCount())
	}
}

// TestRefreshMissingTokenRejected: a same-origin POST with no token is 403 and
// starts no job.
func TestRefreshMissingTokenRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, "", source.Signal)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing-token refresh status = %d, want 403", rec.Code)
	}
	if fe.refreshCount() != 0 {
		t.Fatalf("missing-token refresh started %d jobs, want 0", fe.refreshCount())
	}
}

// TestRefreshInvalidTokenRejected: a same-origin POST with a well-formed but
// never-minted token is 403 and starts no job.
func TestRefreshInvalidTokenRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	bogus := strings.Repeat("ab", 32)
	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, bogus, source.Signal)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invalid-token refresh status = %d, want 403", rec.Code)
	}
	if fe.refreshCount() != 0 {
		t.Fatalf("invalid-token refresh started %d jobs, want 0", fe.refreshCount())
	}
}

// TestRefreshUnknownSourceRejected: a valid same-origin+token POST with a source
// outside the fixed enum is a 400 and starts no job — no client string reaches a
// filesystem path (SPEC-0013 §Security "No arbitrary paths").
func TestRefreshUnknownSourceRejected(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, tok, "../../etc/passwd")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown-source refresh status = %d, want 400", rec.Code)
	}
	if fe.refreshCount() != 0 {
		t.Fatalf("unknown-source refresh started %d jobs, want 0", fe.refreshCount())
	}
}

// TestRefreshHappyPath: a same-origin POST with a valid token and a known source
// starts a refresh job for that source and renders the progress fragment.
func TestRefreshHappyPath(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{progress: onboard.Progress{Phase: onboard.PhaseExporting, Message: "Refreshing Signal…"}}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, tok, source.Signal)
	if rec.Code != http.StatusOK {
		t.Fatalf("happy-path refresh status = %d, want 200", rec.Code)
	}
	if fe.refreshCount() != 1 {
		t.Fatalf("happy-path refresh started %d jobs, want 1", fe.refreshCount())
	}
	if got := fe.refreshedSources(); len(got) != 1 || got[0] != source.Signal {
		t.Fatalf("refresh targeted %v, want [signal]", got)
	}
	body := rec.Body.String()
	if !contains(body, "Refreshing Signal") {
		t.Errorf("refresh progress fragment missing the phase message; got %q", body)
	}
	// The active fragment self-polls /setup/status and offers Cancel — the same
	// aria-live surface Enable uses.
	if !contains(body, `hx-get="/setup/status/signal"`) {
		t.Errorf("active refresh fragment missing the status poller")
	}
}

// TestRefreshNoEnablerUnavailable: with no Enabler wired, a valid refresh POST
// renders the "unavailable" affordance rather than 500ing or starting a job.
func TestRefreshNoEnablerUnavailable(t *testing.T) {
	srv := newEmptyStoreServer(t) // no SetEnabler
	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, tok, source.Signal)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-enabler refresh status = %d, want 200", rec.Code)
	}
	if !contains(rec.Body.String(), "desktop app") {
		t.Errorf("no-enabler refresh fragment missing the unavailable affordance; got %q", rec.Body.String())
	}
}

// configuredServer builds a Server whose three sources are all Enabled (each has a
// configured managed archive root), so /setup renders three Enabled cards and
// sourceConfigured reports true for each.
func configuredServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		DataDir:             t.TempDir(),
		ArchiveRoot:         t.TempDir(),
		IMessageArchiveRoot: t.TempDir(),
		WhatsAppArchiveRoot: t.TempDir(),
	}
	srv, err := NewServer(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

// TestRefreshAllRouteGone: the all-sources Refresh was removed (issue #194 —
// per-source refresh is the whole surface, obsoleting #146). The route must be
// fully gone: a POST is a 404, never a 403 (which would imply the gate — and
// the handler behind it — still exists), and no job starts.
func TestRefreshAllRouteGone(t *testing.T) {
	srv := configuredServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	form := url.Values{setupTokenField: {tok}}
	req := httptest.NewRequest(http.MethodPost, "/setup/refresh-all", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", selfOrigin)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /setup/refresh-all status = %d, want 404 (route removed)", rec.Code)
	}
	if fe.refreshCount() != 0 {
		t.Fatalf("removed refresh-all route started %d jobs, want 0", fe.refreshCount())
	}
}

// TestSetupPageRendersPerSourceRefreshWhenEnabled: an Enabled card renders its
// per-source Refresh control (the surviving Refresh surface after #194), armed
// with the per-session token — and NO all-sources control anywhere.
func TestSetupPageRendersPerSourceRefreshWhenEnabled(t *testing.T) {
	srv := configuredServer(t)
	fe := &fakeEnabler{}
	srv.SetEnabler(fe)

	body := get(t, srv, "/providers").Body.String()
	if !contains(body, `hx-post="/setup/refresh"`) {
		t.Error("/setup Enabled card should render a per-source Refresh control")
	}
	if !contains(body, "X-Setup-Token") {
		t.Error("/setup Refresh controls should carry the per-session token header")
	}
	if contains(body, "refresh-all") || contains(body, "Refresh all sources") {
		t.Error("/setup must not render any all-sources Refresh control (#194)")
	}
}

// TestSetupPageHidesRefreshWhenNoneEnabled: with no source Enabled, no
// per-source Refresh renders.
func TestSetupPageHidesRefreshWhenNoneEnabled(t *testing.T) {
	srv := newEmptyStoreServer(t)
	srv.SetDetector(detectorFor(signalPlusIMessageHome(t), false))
	body := get(t, srv, "/providers").Body.String()
	if contains(body, `hx-post="/setup/refresh"`) {
		t.Error("/setup with no Enabled source should NOT render a per-source Refresh control")
	}
}
