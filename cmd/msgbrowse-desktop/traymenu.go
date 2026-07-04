//go:build desktop

// Menubar status item wiring via fyne.io/systray (design.md decision
// "Menubar residency via a systray companion library" — Wails v2 has no
// first-class systray API).
//
// Loop coexistence: systray.RunWithExternalLoop is the library's documented
// mode for living beside another GUI toolkit's main loop. The returned
// start() is handed to the per-platform scheduleTrayStart (issue #167): on
// macOS it is DEFERRED onto the GCD main queue so the NSStatusItem is created
// on the main thread after [NSApp run] has finished launching — created
// earlier (as #122 did), AppKit can silently never render it, the exact
// no-menubar-icon symptom on real hardware (see tray_platform_darwin.go); on
// Linux it runs immediately, the backend being pure-Go D-Bus
// (StatusNotifierItem), fully independent of Wails' GTK loop. end() is called
// after wails.Run returns.
//
// Observability (issue #167): registration is instrumented through the shell
// notes ring buffer — surfaced on the web app's Logs page — and onReady
// completion closes a ready channel main.go's watchdog races against a
// deadline, so a status item that never appears is a visible error, not a
// silent gap. fyne.io/systray itself reports no registration error; onReady
// is the only success signal it offers.
//
// Headless/Linux caveat (SPEC-0010 story #118): the tray requires a
// StatusNotifier host (a desktop panel). Without one — headless boxes, bare
// window managers — fyne.io/systray logs the D-Bus failure and every menu
// operation degrades to a no-op; the guards below additionally convert any
// panic from that degraded state into a log line + shell note so tray failure
// can never take down the app or its graceful shutdown.
//
// Governing: SPEC-0010 REQ "Menubar residency", REQ "Menubar quick menu".
package main

import (
	_ "embed"
	"time"

	"fyne.io/systray"
	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/shellnotes"
	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/tray"
)

// Tray icons: a msgbrowse speech bubble, 32x32 PNG. The template variant
// (black + alpha) is what macOS recolors for light/dark menubars; the
// regular variant (light slate) is for Linux StatusNotifier panels, which
// are typically dark. fyne.io/systray falls back to the regular icon on
// non-macOS platforms.
var (
	//go:embed trayicon_template.png
	trayIconTemplatePNG []byte
	//go:embed trayicon.png
	trayIconPNG []byte
)

// healthRefreshEvery is the cadence of the MCP status line's health re-poll.
const healthRefreshEvery = 15 * time.Second

// setupTray registers the quick menu and returns start/stop functions per
// the RunWithExternalLoop contract, plus a ready channel closed once the
// library's onReady confirms the status item registered and the quick menu
// was installed — the only success signal systray offers, raced against a
// deadline by main.go's watchdog (issue #167). Start/stop are panic-guarded:
// on a Linux session with no StatusNotifier host the library's teardown
// dereferences the D-Bus connection it never got, and a resident app must
// shrug that off.
func setupTray(m *tray.Menu, notes *shellnotes.Log) (start, stop func(), ready <-chan struct{}) {
	done := make(chan struct{})
	readyCh := make(chan struct{})
	s, e := systray.RunWithExternalLoop(func() {
		trayReady(m, done)
		close(readyCh)
	}, nil)
	start = func() { guard("start", s, notes) }
	stop = func() {
		close(done)
		guard("stop", e, notes)
	}
	return start, stop, readyCh
}

// guard runs f, converting a panic into a log line AND a Logs-page shell note
// — tray degradation must never break the app (SPEC-0010: degrade gracefully
// when no tray host) and must never be silent (issue #167).
func guard(what string, f func(), notes *shellnotes.Log) {
	defer func() {
		if r := recover(); r != nil {
			notes.Errorf("menubar: systray %s panicked (%v) — continuing without a tray", what, r)
		}
	}()
	f()
}

// trayReady builds the quick menu once the status item is registered, then
// hands the click channels to the pure tray.Menu loop. Menu order per the
// SPEC-0010 table: View Messages · Transfer/Pair Device… · MCP status line ·
// Copy MCP Config · Quit.
func trayReady(m *tray.Menu, done <-chan struct{}) {
	systray.SetTemplateIcon(trayIconTemplatePNG, trayIconPNG)
	systray.SetTooltip(tray.Tooltip)

	view := systray.AddMenuItem(tray.ViewMessagesTitle, tray.ViewMessagesTooltip)
	pair := systray.AddMenuItem(tray.PairDeviceTitle, tray.PairDeviceTooltip)
	systray.AddSeparator()
	status := systray.AddMenuItem(tray.StatusTitle(m.Endpoint, m.Actions.Probe()), tray.StatusTooltip)
	config := systray.AddMenuItem(tray.CopyConfigTitle, tray.CopyConfigTooltip)
	systray.AddSeparator()
	quit := systray.AddMenuItem(tray.QuitTitle, tray.QuitTooltip)

	ticker := time.NewTicker(healthRefreshEvery)
	go func() {
		defer ticker.Stop()
		m.Loop(done, tray.Events{
			ViewClicked:   view.ClickedCh,
			PairClicked:   pair.ClickedCh,
			StatusClicked: status.ClickedCh,
			ConfigClicked: config.ClickedCh,
			QuitClicked:   quit.ClickedCh,
			Refresh:       ticker.C,
		}, status, config)
	}()
}
