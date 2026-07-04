package web

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/joestump/msgbrowse/internal/onboard"
	"github.com/joestump/msgbrowse/internal/store"
)

// TestCardReadsEnabledFromStorePresence is the issue #149 acceptance: a source
// with imported conversations in the store reads as ENABLED even when the live
// OS-permission probe would still report Needs-permission and NO config archive
// root is set. Store-presence is the primary Enabled signal, so a successful
// import can never leave the card stuck at "Needs permission".
func TestCardReadsEnabledFromStorePresence(t *testing.T) {
	// A store that HAS Signal conversations (the fixture archive is Signal), but a
	// config with NO archive roots set — so the only thing that can make Signal
	// read Enabled is store-presence, not sourceConfigured.
	archive := filepath.Join("..", "..", "testdata", "archive")
	st, err := store.Open(filepath.Join(t.TempDir(), "web.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := ingest.Run(context.Background(), st, ingest.Options{
		ArchiveRoot: archive,
		Now:         func() time.Time { return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC) },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	cfg := &config.Config{DataDir: t.TempDir()} // NO ArchiveRoot: sourceConfigured(signal) == false
	srv, err := NewServer(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// A detector that reports Signal as Needs-permission (sealed keychain): without
	// the store-presence fix, the card would render "Needs permission" despite the
	// import.
	srv.SetDetector(detectorFor(signalPlusIMessageHome(t), false))

	body := get(t, srv, "/providers").Body.String()
	if !strings.Contains(body, `aria-label="Signal: Enabled"`) {
		t.Error("/providers Signal card should read Enabled from store-presence, not Needs-permission")
	}
	// The contradictory Needs-permission badge must NOT ride on the Signal card.
	if strings.Contains(body, `aria-label="Signal: Needs permission"`) {
		t.Error("/providers Signal card still shows Needs permission after import — the #149 contradiction")
	}
}

// TestEnableDoneOOBSwapsCard proves the Enable Done fragment flips the source's
// card to Enabled out-of-band (issue #149), so the stale "Needs permission" badge
// can't linger after a successful import. It drives the status endpoint with a
// Done job whose store already has conversations (the fixture), so the OOB card
// renders Enabled.
//
// (The former navbar-counts OOB swap is gone: #152 Option A dropped the global
// counts from the toolbar, so there is no counts element to refresh — the sidebar
// OOB swap, covered in sidebar_refresh_test.go, is the post-import payoff that
// remains.)
func TestEnableDoneOOBSwapsCard(t *testing.T) {
	srv, _, _ := newTestServer(t) // fixture store has Signal conversations
	fe := &fakeEnabler{progress: onboard.Progress{
		Phase:   onboard.PhaseDone,
		Message: "Enabled Signal — 1 conversations, 2 messages added",
		Result:  onboard.ImportResult{ConversationsChanged: 1, MessagesAdded: 2},
	}}
	srv.SetEnabler(fe)

	// GET the status fragment for a Done job (safe GET, no token needed).
	rec := get(t, srv, "/setup/status/signal")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	// The OOB card swap flips Signal to Enabled in place.
	if !strings.Contains(body, `id="setup-card-signal"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("Done fragment missing the out-of-band Setup card swap")
	}
	if !strings.Contains(body, `aria-label="Signal: Enabled"`) {
		t.Error("Done fragment OOB card is not in the Enabled state")
	}
	// The toolbar no longer carries global counts (Option A), so the Done fragment
	// must not emit the removed navbar-counts swap.
	if strings.Contains(body, `id="navbar-counts"`) {
		t.Error("Done fragment should no longer emit the removed navbar-counts swap")
	}
}
