package web

import (
	"net/http"
	"testing"

	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/source"
)

// TestEnableDoneEmitsSidebarRefreshTrigger is the #142 fold-in: a successful
// Enable→import Done fragment must emit HX-Trigger: msgbrowse:imported so the
// client refreshes the sidebar and the newly-imported conversations appear
// without a manual nav (SPEC-0013 REQ "the source appears in the transcript
// sidebar").
func TestEnableDoneEmitsSidebarRefreshTrigger(t *testing.T) {
	srv, _, _ := newTestServer(t) // fixture store has conversations for the OOB list
	fe := &fakeEnabler{progress: onboard.Progress{
		Phase:   onboard.PhaseDone,
		Message: "Imported 3 conversations.",
	}}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/enable", selfOrigin, tok, source.IMessage)
	if rec.Code != http.StatusOK {
		t.Fatalf("Done enable status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("HX-Trigger"); got != setupImportedTrigger {
		t.Errorf("Done fragment HX-Trigger = %q, want %q", got, setupImportedTrigger)
	}
}

// TestEnableDoneCarriesSidebarOOBSwap: the Done fragment also piggybacks an
// out-of-band swap of the sidebar conversation list, so the imported
// conversations render immediately even before the trigger-driven refresh (#142,
// "hx-swap-oob the sidebar lists").
func TestEnableDoneCarriesSidebarOOBSwap(t *testing.T) {
	srv, _, _ := newTestServer(t)
	fe := &fakeEnabler{progress: onboard.Progress{
		Phase:   onboard.PhaseDone,
		Message: "Imported.",
	}}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	body := enablePOST(t, srv, "/setup/enable", selfOrigin, tok, source.IMessage).Body.String()
	// The OOB sidebar list is present and marked hx-swap-oob so htmx replaces the
	// live sidebar in place.
	if !contains(body, `id="sidebar-conversations"`) {
		t.Error("Done fragment missing the out-of-band sidebar conversation list")
	}
	if !contains(body, `hx-swap-oob="true"`) {
		t.Error("Done fragment sidebar list missing hx-swap-oob")
	}
	// The fixture archive has conversations, so at least one conv row rides along.
	if !contains(body, "conv-item") {
		t.Error("Done OOB sidebar list carries no conversation rows")
	}
}

// TestEnableActiveDoesNotRefreshSidebar: a non-terminal (Active) progress
// fragment must NOT emit the trigger or the OOB swap — the refresh is only the
// terminal Done payoff, so an in-flight poll does not thrash the sidebar.
func TestEnableActiveDoesNotRefreshSidebar(t *testing.T) {
	srv, _, _ := newTestServer(t)
	fe := &fakeEnabler{progress: onboard.Progress{
		Phase:   onboard.PhaseExporting,
		Message: "Exporting…",
	}}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/enable", selfOrigin, tok, source.IMessage)
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("Active fragment emitted HX-Trigger %q, want none", got)
	}
	if contains(rec.Body.String(), `hx-swap-oob`) {
		t.Error("Active fragment should not carry the OOB sidebar swap")
	}
}
