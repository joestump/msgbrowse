package web

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/setup"
	"github.com/joestump/msgbrowse/internal/source"
)

// permissionFailedProgress scripts a fakeEnabler terminal state shaped like the
// issue #174 real-Mac failure: the export subprocess exited non-zero with a
// permission-shaped stderr, so the runner recorded PhaseFailed wrapping
// onboard.ErrPermissionDenied.
func permissionFailedProgress() onboard.Progress {
	return onboard.Progress{
		Phase:   onboard.PhaseFailed,
		Message: "iMessage export was blocked by macOS — grant access in System Settings, then try again",
		Err:     fmt.Errorf("iMessage export was blocked by macOS: %w (exit status 1)", onboard.ErrPermissionDenied),
	}
}

// assertPermissionGuidanceFragment asserts a progress response re-enters the
// guidance flow (issue #174): an out-of-band Needs-permission card swap
// carrying the existing FDA guidance modal — the exact System Settings deep
// link, the Recheck affordance, and the stale-grant sentence.
func assertPermissionGuidanceFragment(t *testing.T, body string) {
	t.Helper()
	if !contains(body, `id="setup-card-imessage"`) || !contains(body, `hx-swap-oob="true"`) {
		t.Error("permission-shaped failure missing the out-of-band card swap")
	}
	if !contains(body, "setup-card-needs-permission") {
		t.Error("permission-shaped failure did not flip the card to needs-permission")
	}
	if !contains(body, setup.FullDiskAccessDeepLink) {
		t.Errorf("permission-shaped failure missing the Full Disk Access deep link %q", setup.FullDiskAccessDeepLink)
	}
	if !contains(body, `hx-post="/setup/recheck"`) {
		t.Error("permission-shaped failure missing the Recheck affordance")
	}
	// The stale-grant sentence (issue #174): after an app update/replace, macOS
	// may require removing + re-adding msgbrowse under Full Disk Access.
	if !contains(body, "adding it back") {
		t.Error("permission-shaped failure missing the stale-grant guidance sentence")
	}
}

// TestEnablePermissionShapedFailureRendersGuidance: when an Enable job finishes
// Failed with a permission-shaped error, the response re-enters the guidance
// flow instead of the generic failed fragment (issue #174).
func TestEnablePermissionShapedFailureRendersGuidance(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{progress: permissionFailedProgress()}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/enable", selfOrigin, tok, source.IMessage)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable POST status = %d, want 200", rec.Code)
	}
	assertPermissionGuidanceFragment(t, rec.Body.String())
}

// TestRefreshPermissionShapedFailureRendersGuidance: the Refresh path shares
// renderProgress with Enable, so a stale-FDA Refresh failure (the real-Mac
// issue #174 report) re-enters the same guidance flow.
func TestRefreshPermissionShapedFailureRendersGuidance(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{progress: permissionFailedProgress()}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/refresh", selfOrigin, tok, source.IMessage)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh POST status = %d, want 200", rec.Code)
	}
	assertPermissionGuidanceFragment(t, rec.Body.String())
}

// TestStatusPollPermissionShapedFailureRendersGuidance: the aria-live status
// poller lands on the same terminal state, so the polled fragment carries the
// guidance card swap too — the flow re-enters guidance no matter which response
// observes the failure first.
func TestStatusPollPermissionShapedFailureRendersGuidance(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{progress: permissionFailedProgress()}
	srv.SetEnabler(fe)

	rec := get(t, srv, "/setup/status/imessage")
	if rec.Code != http.StatusOK {
		t.Fatalf("status GET = %d, want 200", rec.Code)
	}
	assertPermissionGuidanceFragment(t, rec.Body.String())
}

// TestGenericFailureStaysGenericFailedFragment: a non-permission failure keeps
// today's generic failed fragment — no guidance modal, no deep link, no card
// flip — so unrelated crashes never masquerade as permission problems.
func TestGenericFailureStaysGenericFailedFragment(t *testing.T) {
	srv := newEmptyStoreServer(t)
	fe := &fakeEnabler{progress: onboard.Progress{
		Phase:   onboard.PhaseFailed,
		Message: "iMessage export failed: exit status 2",
		Err:     fmt.Errorf("iMessage export failed: %w (exit status 2)", onboard.ErrExportFailed),
	}}
	srv.SetEnabler(fe)

	tok := mintToken(t, srv)
	rec := enablePOST(t, srv, "/setup/enable", selfOrigin, tok, source.IMessage)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable POST status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !contains(body, "setup-progress-err") || !contains(body, "iMessage export failed") {
		t.Error("generic failure missing the failed progress line")
	}
	for _, forbidden := range []string{
		setup.FullDiskAccessDeepLink,
		"setup-card-needs-permission",
		`hx-swap-oob="true"`,
	} {
		if contains(body, forbidden) {
			t.Errorf("generic failure leaked the permission guidance marker %q", forbidden)
		}
	}
}
