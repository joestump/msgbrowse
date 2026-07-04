//go:build desktop && darwin

// macOS platform glue for the menubar status item and Dock policy (issue
// #167). This file (with tray_platform_darwin.m) is the desktop module's only
// cgo beyond the Wails bindings themselves, and it exists for two reasons:
//
// Status-item timing: fyne.io/systray's external-loop start() creates the
// NSStatusItem synchronously on the calling thread. The #122 wiring called it
// on the main goroutine BEFORE wails.Run — i.e. before [NSApp run] had
// finished launching the app and before Wails' AppDelegate flipped the
// activation policy in applicationWillFinishLaunching. AppKit's documented
// expectation is that status items are installed once the app has finished
// launching (Apple's NSStatusBar guidance; the same reason getlantern/systray
// creates its item inside applicationDidFinishLaunching) — installed earlier,
// the item can silently never render, which is exactly the no-menubar-icon
// symptom on real hardware while headless CI stayed green. scheduleTrayStart
// therefore defers start() onto the GCD main queue: the block runs on the
// main thread (NSStatusItem's thread requirement) on the first turn of the
// run loop wails.Run starts — strictly after applicationDidFinishLaunching.
// The Go side keeps runtime.LockOSThread semantics intact (systray's init
// locks the main goroutine; the callback rides the same OS thread).
//
// Dock policy: SPEC-0010 menubar residency + the owner's "hide up in the
// menubar" preference. When close-to-tray hides the app (Wails'
// HideWindowOnClose runs [NSApp hide:]), the NSApplicationDidHideNotification
// observer switches the app to the accessory activation policy — no Dock
// icon, no Cmd+Tab entry, menubar item only. View Messages restores the
// regular policy before unhiding (shell.showWindow → setDockVisible(true)).
// The observer is armed ONLY after the tray watchdog confirms the status item
// registered (main.go watchTray): if the tray is broken, hiding the Dock icon
// would strand the user with no way back.
//
// Everything here dispatches through the main queue, so any goroutine may
// call these safely.
package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

// Definitions live in tray_platform_darwin.m — a file that uses //export must
// keep its preamble to declarations only (cgo rule).
void msgbrowse_schedule_tray_start(void);
void msgbrowse_set_activation_policy(int accessory);
void msgbrowse_enable_dock_autohide(void);
*/
import "C"

// trayStartCh hands the deferred start closure to the main-queue callback.
// Buffered so scheduling never blocks; channel semantics provide the
// happens-before edge between the scheduling goroutine and the main thread.
var trayStartCh = make(chan func(), 1)

//export msgbrowseTrayStartCallback
func msgbrowseTrayStartCallback() {
	select {
	case start := <-trayStartCh:
		start()
	default:
		// Nothing scheduled (defensive; the block is only enqueued by
		// scheduleTrayStart after the send).
	}
}

// scheduleTrayStart queues the systray start onto the GCD main queue, where
// it runs on the main thread after [NSApp run] has finished launching — the
// documented-safe moment to install an NSStatusItem (see the file comment).
func scheduleTrayStart(start func()) {
	trayStartCh <- start
	C.msgbrowse_schedule_tray_start()
}

// setDockVisible switches the app between the regular activation policy
// (Dock icon, Cmd+Tab) and accessory (menubar-only). Restoring visibility
// also activates the app so the reappearing window actually comes frontmost.
func setDockVisible(visible bool) {
	if visible {
		C.msgbrowse_set_activation_policy(C.int(0))
	} else {
		C.msgbrowse_set_activation_policy(C.int(1))
	}
}

// enableDockAutoHide arms the hide-observer: from now on, hiding the app
// (close-to-tray's [NSApp hide:], or Cmd+H — both mean "get out of my way"
// under menubar residency) drops the Dock icon via the accessory policy.
// Call only once the tray is confirmed present (main.go watchTray).
func enableDockAutoHide() {
	C.msgbrowse_enable_dock_autohide()
}
