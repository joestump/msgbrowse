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

// TestGuidanceForUnknownSourceIsEmpty: an unknown source yields empty guidance,
// so the UI can safely gate on it.
func TestGuidanceForUnknownSourceIsEmpty(t *testing.T) {
	g := GuidanceFor("bogus")
	if g.Title != "" || g.SettingsURL != "" || len(g.Steps) != 0 {
		t.Errorf("unknown-source guidance should be empty; got %+v", g)
	}
}
