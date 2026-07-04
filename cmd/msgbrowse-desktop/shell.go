//go:build desktop

// Native shell affordances and lifecycle wiring for the desktop window: the
// application menu, quit bookkeeping, window restore, the /settings deep
// link, and the native clipboard the tray uses with the window closed.
//
// The state decisions live in internal/shellstate (pure, headlessly tested);
// this file binds them to the Wails runtime. The pre-startup quit latch from
// #114's review lives there too: a signal arriving before OnStartup is
// recorded and replayed by startup() instead of being dropped.
//
// Governing: ADR-0017, SPEC-0010 REQ "Native shell affordances", REQ
// "Menubar residency" (explicit quit only; View Messages restores), REQ
// "Menubar quick menu" (clipboard via the native API, window closed), REQ
// "Graceful shutdown" (every quit path funnels into one context cancel).
package main

import (
	"context"
	"fmt"
	goruntime "runtime"

	"github.com/joestump/msgbrowse/cmd/msgbrowse-desktop/internal/shellstate"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// settingsPath is the deep-link target for "Transfer / Pair Device…". The
// /settings Connect page is #100's story and the pairing section within it
// arrives with the device-sync surfaces (#105); until they land the webview
// shows the app's not-found page — documented placeholder behavior, the menu
// wiring is already correct. When the pairing section exists this becomes
// "/settings#pairing".
const settingsPath = "/settings"

// shell binds the pure lifecycle state to the Wails runtime so quit requests
// arriving from any goroutine (menu callback, tray click, signal watcher,
// dead-server watcher) close the app exactly once, at any point in the app
// lifecycle.
type shell struct {
	lc      shellstate.Lifecycle
	baseURL string // embedded server base URL, for deep links
}

func newShell(baseURL string) *shell {
	return &shell{baseURL: baseURL}
}

// startup captures the Wails runtime context and replays a latched
// pre-startup quit: a SIGINT/SIGTERM (or embedded-server death) delivered
// between embedded.Start and OnStartup fires here instead of being dropped.
func (sh *shell) startup(ctx context.Context) {
	if fire := sh.lc.SetContext(ctx); fire != nil {
		wailsruntime.Quit(fire)
	}
}

// shutdown records OnShutdown, making any later quit() a no-op.
func (sh *shell) shutdown(context.Context) {
	sh.lc.MarkShutdown()
}

// quit requests application exit, once. Before OnStartup the request is
// latched and startup replays it.
func (sh *shell) quit() {
	if fire := sh.lc.RequestQuit(); fire != nil {
		wailsruntime.Quit(fire)
	}
}

// showWindow restores the hidden main window (SPEC-0010 "Close-to-tray":
// View Messages brings it back). Show unhides the application on macOS —
// close-to-tray hides the whole app there — and WindowShow orders the window
// front and focuses it everywhere.
func (sh *shell) showWindow() {
	ctx := sh.lc.Context()
	if ctx == nil {
		return // tray clicked before OnStartup; nothing to show yet
	}
	// Restore the Dock icon first (issue #167): close-to-tray may have
	// switched the app to the accessory activation policy (no Dock icon, no
	// Cmd+Tab), and an unhidden window under the accessory policy would not
	// come frontmost. Both calls dispatch onto the macOS main queue in FIFO
	// order, so the policy flip lands before the unhide. No-op off macOS and
	// when the policy is already regular.
	setDockVisible(true)
	wailsruntime.Show(ctx)
	wailsruntime.WindowShow(ctx)
}

// openPairing opens the window deep-linked at the settings pairing surface
// (SPEC-0010 scenario "Pairing from the tray"). Navigation runs through the
// webview's host-side JS injection, which the page CSP does not govern —
// the strict CSP posture on served content is unchanged.
func (sh *shell) openPairing() {
	ctx := sh.lc.Context()
	if ctx == nil {
		return
	}
	sh.showWindow()
	wailsruntime.WindowExecJS(ctx, fmt.Sprintf("window.location.assign(%q);", sh.baseURL+settingsPath))
}

// copyText puts text on the native clipboard via the Wails runtime — the OS
// clipboard API under the hood, functional while the window is hidden
// (SPEC-0010: clipboard actions MUST work with the main window closed). It
// reports success so the tray only acknowledges copies that happened.
func (sh *shell) copyText(text string) bool {
	ctx := sh.lc.Context()
	if ctx == nil {
		return false
	}
	return wailsruntime.ClipboardSetText(ctx, text) == nil
}

// menu builds the native application menu. On macOS the standard app menu
// supplies Cmd+Q quit (and the Edit menu makes clipboard shortcuts reach the
// webview); everywhere a File menu carries the platform's conventional quit
// accelerator. All quit paths call sh.quit(), which funnels into the single
// shutdown context in run().
func (sh *shell) menu() *menu.Menu {
	m := menu.NewMenu()
	if goruntime.GOOS == "darwin" {
		m.Append(menu.AppMenu())
		m.Append(menu.EditMenu())
	}
	file := m.AddSubmenu("File")
	file.AddText("Quit msgbrowse", keys.CmdOrCtrl("q"), func(*menu.CallbackData) {
		sh.quit()
	})
	return m
}
