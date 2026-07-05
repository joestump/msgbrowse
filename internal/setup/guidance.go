// Permission-guidance content: the step-by-step instructions and the exact
// macOS System Settings deep link a source needs when its OS-consent probe
// reports Needs-permission (SPEC-0013 REQ "Permission detection and guidance").
//
// This is detect-and-guide only (ADR-0020 decision 2): the guidance NEVER
// acquires, bypasses, or spoofs a grant — it names the exact pane and tells the
// user what to do, then the Recheck action (#134) re-runs the probe. The content
// is fixed, app-owned data (no request-derived values), so the UI renders it
// through html/template escaping like every other server-computed string.
//
// It lives beside the probes because it describes the same OS gate each probe
// checks: Full Disk Access for the iMessage chat.db, and Signal Desktop's
// Keychain "Always Allow" grant. WhatsApp's container gate has no distinct
// pane-level deep link (it is covered by Full Disk Access on the container), so
// it reuses the Full Disk Access guidance.
//
// Governing: ADR-0020 (OS consent gates are DETECT-AND-GUIDE only; deep-link the
// exact System Settings pane, never bypass consent), SPEC-0013 REQ "Permission
// detection and guidance".
package setup

import "github.com/joestump/msgbrowse/internal/source"

// The exact macOS System Settings deep links. macOS honors the
// `x-apple.systempreferences:` URL scheme to open a specific Privacy & Security
// pane. Full Disk Access is `Privacy_AllFilesAccess`; opening it drops the user
// on the exact list they must add msgbrowse to.
const (
	// FullDiskAccessDeepLink opens System Settings → Privacy & Security → Full
	// Disk Access. This is the pane the iMessage chat.db (and the WhatsApp group
	// container) read requires.
	FullDiskAccessDeepLink = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFilesAccess"
)

// Guidance is the fixed, app-owned content for guiding a user through granting
// one source's missing OS consent. Every field is a constant string chosen by
// the app — nothing here is request-derived — so it is safe to render through
// html/template escaping.
type Guidance struct {
	// Source is the source id the guidance concerns (source.Signal, .IMessage,
	// .WhatsApp).
	Source string
	// Title names the grant ("Full Disk Access", "Signal Keychain access").
	Title string
	// Summary is a one-line explanation of why the grant is needed and exactly
	// what the app will read once it is granted.
	Summary string
	// Steps are the ordered instructions to grant it. Rendered as a numbered list.
	Steps []string
	// SettingsURL is the exact System Settings deep link to open the relevant
	// pane, or "" when the grant has no settings pane (the Signal Keychain case,
	// where the "Always Allow" prompt is answered inline, not in Settings).
	SettingsURL string
	// SettingsLabel is the accessible label/anchor text for the deep-link control,
	// or "" when SettingsURL is empty.
	SettingsLabel string
}

// fullDiskAccessGuidance is the Full Disk Access guidance for the iMessage
// chat.db. It names the exact file the app reads (never a bypass) and deep-links
// the exact System Settings pane.
func fullDiskAccessGuidance(src string) Guidance {
	return Guidance{
		Source:  src,
		Title:   "Full Disk Access",
		Summary: "macOS protects the Messages database. Grant msgbrowse Full Disk Access so it can read ~/Library/Messages/chat.db — nothing leaves this machine.",
		Steps: []string{
			"Open System Settings → Privacy & Security → Full Disk Access (use the button below).",
			"Turn on the switch for msgbrowse. If it is not listed, click + and add it.",
			"Return here and click Recheck.",
		},
		SettingsURL:   FullDiskAccessDeepLink,
		SettingsLabel: "Open Full Disk Access settings",
	}
}

// signalKeychainGuidance guides the Signal Desktop Keychain "Always Allow"
// prompt. There is no Settings pane to deep-link: the grant is answered inline
// at the OS prompt the first time the export reads the sealed key, so the
// guidance explains the prompt behavior instead of linking a pane.
func signalKeychainGuidance() Guidance {
	return Guidance{
		Source:  source.Signal,
		Title:   "Signal Keychain access",
		Summary: "Signal Desktop seals its database key in the macOS Keychain. On first export, macOS asks whether msgbrowse may use that key — choose Always Allow so future refreshes do not prompt again.",
		Steps: []string{
			"Click Enable to start the Signal export.",
			"When macOS shows the Keychain prompt for the Signal key, click Always Allow (not just Allow).",
			"If you already clicked Deny, return here and click Recheck after re-running Enable.",
		},
		// No Settings deep link: the Keychain grant is an inline OS prompt, not a
		// Privacy pane toggle. Never bypass it — guide the prompt behavior.
		SettingsURL:   "",
		SettingsLabel: "",
	}
}

// staleGrantNote is the extra Full Disk Access sentence for the export-failure
// path (issue #174): macOS ties an FDA grant to the app's code signature, so
// updating or replacing the app can silently invalidate a grant that still
// shows as enabled in System Settings. The sentence tells the user the
// remove-and-re-add step — detect-and-guide only, nothing here touches TCC.
const staleGrantNote = "After updating or replacing msgbrowse, macOS may require removing it from Full Disk Access and adding it back before the grant applies again."

// GuidanceForExportFailure returns the permission guidance for a source whose
// EXPORT run failed with a permission-shaped error (issue #174) — the same
// fixed content GuidanceFor returns, with the stale-grant sentence appended to
// the Full Disk Access summary, because in this path a grant that LOOKS enabled
// may be bound to a replaced binary. Signal's Keychain guidance is returned
// unchanged (its grant is not the signature-bound FDA toggle).
func GuidanceForExportFailure(src string) Guidance {
	g := GuidanceFor(src)
	if g.SettingsURL == FullDiskAccessDeepLink {
		g.Summary += " " + staleGrantNote
	}
	return g
}

// GuidanceFor returns the permission guidance for a source's missing OS-consent
// grant. The caller shows it only when the source's probe reports
// PermissionNeeded; the content is stable per source so the UI can render it
// deterministically. An unknown source returns the zero Guidance (empty), which
// the UI treats as "no specific guidance".
func GuidanceFor(src string) Guidance {
	switch src {
	case source.IMessage:
		return fullDiskAccessGuidance(src)
	case source.Signal:
		return signalKeychainGuidance()
	case source.WhatsApp:
		// WhatsApp's container read is covered by the same Full Disk Access grant;
		// there is no distinct pane, so reuse the FDA guidance (retitled for the
		// container it unlocks).
		g := fullDiskAccessGuidance(src)
		g.Summary = "macOS protects the WhatsApp app's data container. Grant msgbrowse Full Disk Access so it can read the WhatsApp ChatStorage.sqlite — nothing leaves this machine."
		return g
	default:
		return Guidance{Source: src}
	}
}
