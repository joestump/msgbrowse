package setup

import (
	"strings"
	"testing"

	"github.com/joestump/msgbrowse/internal/source"
)

// TestGuidanceForFullDiskAccess: the iMessage guidance names Full Disk Access,
// references the exact chat.db path, and carries the exact System Settings deep
// link the spec pins (#134).
func TestGuidanceForFullDiskAccess(t *testing.T) {
	g := GuidanceFor(source.IMessage)
	if g.Title != "Full Disk Access" {
		t.Errorf("iMessage guidance title = %q, want Full Disk Access", g.Title)
	}
	if g.SettingsURL != FullDiskAccessDeepLink {
		t.Errorf("iMessage settings URL = %q, want %q", g.SettingsURL, FullDiskAccessDeepLink)
	}
	if !strings.Contains(g.SettingsURL, "Privacy_AllFilesAccess") {
		t.Errorf("iMessage deep link is not the Full Disk Access pane: %q", g.SettingsURL)
	}
	if !strings.Contains(g.Summary, "chat.db") {
		t.Errorf("iMessage guidance summary should name chat.db; got %q", g.Summary)
	}
	if len(g.Steps) == 0 {
		t.Error("iMessage guidance has no steps")
	}
	if g.SettingsLabel == "" {
		t.Error("iMessage guidance with a settings URL must carry a label")
	}
}

// TestGuidanceForSignalKeychain: Signal's guidance covers the Keychain "Always
// Allow" prompt and has NO Settings deep link (the grant is an inline OS prompt,
// not a Privacy pane toggle) — the app must guide, never bypass.
func TestGuidanceForSignalKeychain(t *testing.T) {
	g := GuidanceFor(source.Signal)
	if g.Title != "Signal Keychain access" {
		t.Errorf("Signal guidance title = %q, want Signal Keychain access", g.Title)
	}
	if g.SettingsURL != "" {
		t.Errorf("Signal guidance must have no Settings deep link (Keychain prompt is inline); got %q", g.SettingsURL)
	}
	joined := strings.ToLower(strings.Join(g.Steps, " ") + " " + g.Summary)
	if !strings.Contains(joined, "always allow") {
		t.Errorf("Signal guidance should mention the Always Allow prompt; got steps %v", g.Steps)
	}
}

// TestGuidanceForWhatsApp: WhatsApp's container read is covered by Full Disk
// Access, so it reuses the FDA deep link but names the WhatsApp container.
func TestGuidanceForWhatsApp(t *testing.T) {
	g := GuidanceFor(source.WhatsApp)
	if g.SettingsURL != FullDiskAccessDeepLink {
		t.Errorf("WhatsApp settings URL = %q, want the Full Disk Access deep link", g.SettingsURL)
	}
	if !strings.Contains(strings.ToLower(g.Summary), "whatsapp") {
		t.Errorf("WhatsApp guidance summary should name WhatsApp; got %q", g.Summary)
	}
}

// TestGuidanceForExportFailureAddsStaleGrantNote: the export-failure variant
// (issue #174) appends the stale-grant sentence to the Full Disk Access
// guidance — after an app update/replace, macOS may require removing and
// re-adding msgbrowse before the grant applies — for the FDA sources (iMessage,
// WhatsApp) while leaving everything else (steps, deep link) identical.
func TestGuidanceForExportFailureAddsStaleGrantNote(t *testing.T) {
	for _, src := range []string{source.IMessage, source.WhatsApp} {
		base := GuidanceFor(src)
		g := GuidanceForExportFailure(src)
		if !strings.Contains(g.Summary, "adding it back") {
			t.Errorf("%s export-failure guidance missing the stale-grant sentence; got %q", src, g.Summary)
		}
		if !strings.HasPrefix(g.Summary, base.Summary) {
			t.Errorf("%s export-failure guidance should extend the base summary, not replace it", src)
		}
		if g.SettingsURL != base.SettingsURL || len(g.Steps) != len(base.Steps) {
			t.Errorf("%s export-failure guidance changed more than the summary", src)
		}
	}
}

// TestGuidanceForExportFailureSignalUnchanged: Signal's Keychain guidance has no
// FDA pane, so the export-failure variant returns it byte-for-byte unchanged.
func TestGuidanceForExportFailureSignalUnchanged(t *testing.T) {
	base := GuidanceFor(source.Signal)
	g := GuidanceForExportFailure(source.Signal)
	if g.Summary != base.Summary || g.SettingsURL != base.SettingsURL {
		t.Errorf("Signal export-failure guidance should be unchanged; got %+v", g)
	}
}

// TestGuidanceForUnknownSourceIsEmpty: an unknown source yields empty guidance,
// so the UI can safely gate on it.
func TestGuidanceForUnknownSourceIsEmpty(t *testing.T) {
	g := GuidanceFor("bogus")
	if g.Title != "" || g.SettingsURL != "" || len(g.Steps) != 0 {
		t.Errorf("unknown-source guidance should be empty; got %+v", g)
	}
}
