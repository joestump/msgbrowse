// The in-app Setup surface: one card per messaging source (Signal / iMessage /
// WhatsApp) with a computed state — Enabled / Ready / Needs-permission /
// Not-detected — read from the shared internal/setup detection + permission
// probes plus whether that source already has a configured/imported archive.
// This story is READ-ONLY: it renders the detection cards and drives first-run
// vs returning routing. The privileged Enable/Recheck/Refresh POSTs are separate
// stories (#133/#134/#135), so nothing here spawns a subprocess or mutates state.
//
// Governing: ADR-0020 (self-contained desktop onboarding — the in-app Setup
// surface that renders the source-detection cards; OS consent is detect-and-guide
// only), SPEC-0013 REQ "First-run wizard versus returning launch", REQ "Source
// detection", REQ "Permission detection and guidance", and the §Accessibility
// Requirements (single h1, ARIA landmarks, aria-labels on card icons, state as
// text not color alone).
package web

import (
	"net/http"

	"github.com/joestump/msgbrowse/internal/setup"
	"github.com/joestump/msgbrowse/internal/source"
)

// Card states, rendered as text (never color alone — SPEC-0013 §Accessibility
// "state MUST be conveyed as text or an accessible name, not by color alone").
// The lowercase token also drives the CSS state class (.setup-card-<state>).
const (
	// setupStateEnabled: the source already has a configured/imported archive —
	// it is live in the app. Terminal, non-actionable in this read-only story.
	setupStateEnabled = "enabled"
	// setupStateReady: detected and accessible — actionable, an Enable would run
	// (in the follow-on flow, #134).
	setupStateReady = "ready"
	// setupStateNeedsPermission: detected but an OS consent grant is missing (Full
	// Disk Access / Signal Keychain / WhatsApp container). The follow-on flow
	// (#134) renders the System Settings guidance + Recheck; here it is surfaced
	// as text so a returning user knows what is blocking the source.
	setupStateNeedsPermission = "needs-permission"
	// setupStateNotDetected: the source's well-known local store is absent (always
	// the case off macOS, and for a source the user does not have installed).
	setupStateNotDetected = "not-detected"
)

// enableButtonCtx pairs a render's Enable availability + per-session token with
// one card's source, so the shared "setup-enable-button" define can decide
// live-vs-disabled and carry the token. html/template has no struct literals, so
// the enableButton FuncMap adapter builds it inside the card range.
type enableButtonCtx struct {
	Available bool
	Token     string
	Source    string
}

// enableButton is the FuncMap adapter that builds an enableButtonCtx in the
// setup template's card range.
func enableButton(available bool, token, src string) enableButtonCtx {
	return enableButtonCtx{Available: available, Token: token, Source: src}
}

// setupCard is one source's Setup card: everything the template needs to render
// the card and its (read-only in this story) action affordance. Every field is a
// server-computed value or a fixed constant — no request-derived content — so
// html/template escaping is the only encoding needed.
type setupCard struct {
	// Source is the fixed source id (source.Signal / .IMessage / .WhatsApp).
	Source string
	// Label is the human display name ("Signal", "iMessage", "WhatsApp").
	Label string
	// State is one of the setupState* tokens above; drives both the text badge
	// and the .setup-card-<State> style class.
	State string
	// StateLabel is the human badge text for State ("Ready", "Needs permission",
	// "Not detected", "Enabled").
	StateLabel string
	// Detail is a one-line human explanation of the state (what was found, or what
	// is blocking) — the accessible, color-independent state description.
	Detail string
	// Actionable is true for Ready / Needs-permission — the states where a future
	// Enable/Recheck applies. Enabled and Not-detected render no primary action.
	Actionable bool
}

// setupData drives the /setup page. It embeds baseData so the shell (navbar +
// sidebar) renders in the full document, and carries the per-source cards plus a
// count of actionable cards for the intro copy.
type setupData struct {
	baseData
	// Cards is one card per supported source, in source.All order (the SPEC-0013
	// "one card per source" contract).
	Cards []setupCard
	// AnyActionable is true when at least one card is Ready or Needs-permission,
	// so the intro can nudge the user toward an action (vs. a pure returning-user
	// view where everything is Enabled/Not-detected).
	AnyActionable bool
	// EnableAvailable reports whether an Enabler is wired (desktop bundle or a
	// configured $PATH resolver): true renders live Enable buttons, false renders
	// the "desktop app required / configure tools" affordance (SPEC-0013).
	EnableAvailable bool
	// Token is a fresh per-session token minted for this render, embedded in the
	// page and submitted with every privileged Setup POST (SPEC-0013 §Security).
	Token string
}

// handleSetup renders the Setup surface: the per-source detection cards. GET-only
// (the route pattern enforces it); it performs read-only filesystem detection and
// permission probes via internal/setup and never mutates state or spawns a
// subprocess (SPEC-0013 §Security "GET routes are safe … no mutation").
//
// It follows the SPEC-0008 *_content partial pattern: a boosted navigation
// (HX-Request) gets only <title> + #main-content via the setup_content define, so
// no sidebar markup or store work rides along.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var base baseData
	if isPartialRequest(r) {
		// Boosted swap: no sidebar listing, no store work (SPEC-0008 REQ-0008-006).
		base = partialBase("Setup · msgbrowse", 0)
	} else {
		var err error
		base, err = s.baseData(r.Context(), "Setup · msgbrowse", 0)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}

	cards := s.setupCards()
	anyActionable := false
	for _, c := range cards {
		if c.Actionable {
			anyActionable = true
			break
		}
	}
	// Mint a fresh per-session token for the privileged Setup POSTs this page can
	// trigger (SPEC-0013 §Security: minted at /setup render, submitted with the
	// POST). A mint failure (crypto/rand) is a real server error, not a silent
	// degrade — an Enable without a token would then always 403.
	token, err := s.setupTokens.mint()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "setup", setupData{
		baseData:        base,
		Cards:           cards,
		AnyActionable:   anyActionable,
		EnableAvailable: s.enableAvailable(),
		Token:           token,
	})
}

// setupCards computes one card per supported source from the shared detection +
// permission probes and the app-owned "already configured" signal. The result is
// in source.All order so the page always renders exactly three cards in a stable
// order.
//
// State precedence (SPEC-0013 REQ "Source detection" four states):
//   - Enabled       — the source already has a configured/imported archive
//     (config archive root set); it is live, so detection is moot.
//   - Not-detected  — the well-known local store is absent.
//   - Needs-permission — detected, but the OS consent probe reports Needed.
//   - Ready         — detected and accessible.
func (s *Server) setupCards() []setupCard {
	det := s.detector()
	cards := make([]setupCard, 0, len(source.All))
	for _, src := range source.All {
		cards = append(cards, s.setupCardFor(det, src))
	}
	return cards
}

// setupCardFor builds a single source's card. Enabled short-circuits detection:
// a configured archive means the source is already live in the app regardless of
// what the current machine's filesystem probes report (the archive may have been
// imported on another run/machine).
func (s *Server) setupCardFor(det setup.Detector, src string) setupCard {
	card := setupCard{Source: src, Label: source.Label(src)}

	if s.sourceConfigured(src) {
		card.State = setupStateEnabled
		card.StateLabel = "Enabled"
		card.Detail = "This source is enabled and its archive is imported."
		card.Actionable = false
		return card
	}

	detection := detectSource(det, src)
	if detection.State != setup.Detected {
		card.State = setupStateNotDetected
		card.StateLabel = "Not detected"
		card.Detail = "No " + source.Label(src) + " data was found on this machine."
		card.Actionable = false
		return card
	}

	switch probeSource(det, src).State {
	case setup.PermissionNeeded:
		card.State = setupStateNeedsPermission
		card.StateLabel = "Needs permission"
		card.Detail = source.Label(src) + " was found, but macOS has not granted access yet."
		card.Actionable = true
	default:
		// PermissionOK or PermissionNotApplicable: the source is present and
		// accessible (a source with no applicable OS gate, e.g. Signal without a
		// sealed key, reports NotApplicable and is Ready).
		card.State = setupStateReady
		card.StateLabel = "Ready"
		card.Detail = source.Label(src) + " was found and is ready to enable."
		card.Actionable = true
	}
	return card
}

// detectSource runs the source-appropriate presence detection.
func detectSource(det setup.Detector, src string) setup.Detection {
	switch src {
	case source.Signal:
		return det.DetectSignal()
	case source.IMessage:
		return det.DetectIMessage()
	case source.WhatsApp:
		return det.DetectWhatsApp()
	default:
		return setup.Detection{Source: src, State: setup.NotDetected}
	}
}

// probeSource runs the source-appropriate OS-permission probe.
func probeSource(det setup.Detector, src string) setup.PermissionProbe {
	switch src {
	case source.Signal:
		return det.ProbeSignalKeychain()
	case source.IMessage:
		return det.ProbeFullDiskAccess()
	case source.WhatsApp:
		return det.ProbeWhatsAppContainer()
	default:
		return setup.PermissionProbe{Source: src, State: setup.PermissionNotApplicable}
	}
}

// sourceConfigured reports whether a source already has a configured archive
// root — the app-owned "Enabled" signal. It reads only the server's own captured
// config values, never a request-derived path.
func (s *Server) sourceConfigured(src string) bool {
	switch src {
	case source.Signal:
		return s.roots.Signal != ""
	case source.IMessage:
		return s.roots.IMessage != ""
	case source.WhatsApp:
		return s.roots.WhatsApp != ""
	default:
		return false
	}
}

// detector returns the Server's source detector, defaulting to a real
// HOME-rooted one. Tests inject a faked Detector (a temp HOME + faked
// Stat/Open/Keychain) via SetDetector to drive each card state deterministically
// on a non-macOS CI box.
func (s *Server) detector() setup.Detector {
	if s.setupDetector != nil {
		return *s.setupDetector
	}
	return setup.NewDetector()
}

// SetDetector overrides the source detector used by /setup. Call it after
// NewServer and before serving; handlers read the field without locking, so late
// wiring would race. It exists for tests (faked HOME) and for the desktop layer
// (#134) to inject the genuine macOS Keychain check.
func (s *Server) SetDetector(d setup.Detector) { s.setupDetector = &d }
