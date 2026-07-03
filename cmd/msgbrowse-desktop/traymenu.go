//go:build desktop

// Menubar status item wiring via fyne.io/systray (design.md decision
// "Menubar residency via a systray companion library" — Wails v2 has no
// first-class systray API).
//
// Loop coexistence: systray.RunWithExternalLoop is the library's documented
// mode for living beside another GUI toolkit's main loop. The returned
// start() is called on the main goroutine *before* wails.Run — on macOS that
// is the main thread, where the NSStatusItem must be created, and the item's
// later menu updates dispatch onto the NSApplication run loop Wails owns; on
// Linux the backend is pure-Go D-Bus (StatusNotifierItem), fully independent
// of Wails' GTK loop. end() is called after wails.Run returns.
//
// Headless/Linux caveat (SPEC-0010 story #118): the tray requires a
// StatusNotifier host (a desktop panel). Without one — headless boxes, bare
// window managers — fyne.io/systray logs the D-Bus failure and every menu
// operation degrades to a no-op; the guards below additionally convert any
// teardown panic from that degraded state into a log line so tray failure
// can never take down the app or its graceful shutdown.
//
// Governing: SPEC-0010 REQ "Menubar residency", REQ "Menubar quick menu".
package main

import (
	_ "embed"
	"log/slog"
	"time"

	"fyne.io/systray"
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
// the RunWithExternalLoop contract. Both are panic-guarded: on a Linux
// session with no StatusNotifier host the library's teardown dereferences
// the D-Bus connection it never got, and a resident app must shrug that off.
func setupTray(m *tray.Menu) (start, stop func()) {
	done := make(chan struct{})
	s, e := systray.RunWithExternalLoop(func() { trayReady(m, done) }, nil)
	start = func() { guard("start", s) }
	stop = func() {
		close(done)
		guard("stop", e)
	}
	return start, stop
}

// guard runs f, converting a panic into a log line — tray degradation must
// never break the app (SPEC-0010: degrade gracefully when no tray host).
func guard(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("systray unavailable; continuing without a tray", "during", what, "cause", r)
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
