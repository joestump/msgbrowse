// Package tray holds the pure parts of the menubar quick menu: item titles,
// the MCP status-line formatting, and the copy-acknowledgment titles. It
// imports neither systray nor Wails, so `make desktop-test` verifies the
// strings and state formatting under CGO_ENABLED=0 on headless machines; the
// tagged wiring in the command root binds these to fyne.io/systray items.
//
// Governing: SPEC-0010 REQ "Menubar quick menu" (menu contents, status line
// running/degraded, copy acknowledgment).
package tray

import (
	"fmt"
	"time"
)

// Menu item titles, in menu order. PairDeviceTitle deep-links to the
// /settings Connect page (SPEC-0010); the pairing section itself lands with
// the Connect-page and device-sync stories (#100/#105).
const (
	ViewMessagesTitle = "View Messages"
	PairDeviceTitle   = "Transfer / Pair Device…"
	CopyConfigTitle   = "Copy MCP Config"
	QuitTitle         = "Quit msgbrowse"
	Tooltip           = "msgbrowse"
)

// Tooltips for the actionable items.
const (
	ViewMessagesTooltip = "Open the msgbrowse window"
	PairDeviceTooltip   = "Open Settings to pair another device"
	StatusTooltip       = "Click to copy the MCP endpoint URL"
	CopyConfigTooltip   = "Copy the MCP client configuration JSON"
	QuitTooltip         = "Quit msgbrowse and stop the embedded server"
)

// StatusTitle renders the MCP status line: the endpoint (which carries the
// live port) plus the server state — "running", or "degraded" when the
// embedded server stops answering its health ping (SPEC-0010 "Status
// accuracy").
func StatusTitle(endpoint string, healthy bool) string {
	state := "running"
	if !healthy {
		state = "degraded"
	}
	return fmt.Sprintf("MCP: %s — %s", endpoint, state)
}

// StatusCopiedTitle is the brief acknowledgment shown after the status item
// copies the MCP endpoint URL (SPEC-0010 scenario "Copy MCP endpoint from
// the tray": the item acknowledges with a retitle).
func StatusCopiedTitle() string {
	return "Copied MCP endpoint ✓"
}

// ConfigCopiedTitle is the brief acknowledgment shown after Copy MCP Config
// lands the JSON block on the clipboard.
func ConfigCopiedTitle() string {
	return "Copied MCP Config ✓"
}

// Item is the minimal menu-item surface the menu loop drives; implemented by
// *systray.MenuItem and by test fakes.
type Item interface {
	SetTitle(string)
}

// Events carries the click streams the loop reacts to. In the shell wiring
// these are the systray items' ClickedCh channels; tests drive them directly.
type Events struct {
	ViewClicked   <-chan struct{}
	PairClicked   <-chan struct{}
	StatusClicked <-chan struct{}
	ConfigClicked <-chan struct{}
	QuitClicked   <-chan struct{}
	Refresh       <-chan time.Time // health re-poll ticks (caller owns cadence)
}

// Actions are the shell operations the menu invokes. CopyText reports
// whether the text actually landed on the clipboard, so the acknowledgment
// retitle never lies (SPEC-0010: the item acknowledges the copy).
type Actions struct {
	ShowWindow  func()
	OpenPairing func()
	CopyText    func(string) bool
	Quit        func()
	Probe       func() bool
}

// Menu is the quick menu's behavior, separated from systray so the plumbing
// is headlessly testable: payloads in (endpoint, config JSON), titles out.
type Menu struct {
	Endpoint   string // MCP endpoint URL; the status item copies it verbatim
	ConfigJSON string // full JSON client-configuration block
	Actions    Actions

	// AckDuration is how long a "Copied ✓" retitle lasts before reverting.
	// Zero means DefaultAckDuration.
	AckDuration time.Duration

	// After is the timer source for acknowledgment reverts; nil means
	// time.After. Tests inject a manual channel to step time deterministically.
	After func(time.Duration) <-chan time.Time
}

// DefaultAckDuration is how long copy acknowledgments stay visible.
const DefaultAckDuration = 2 * time.Second

// Loop drives the menu until done is closed: clicks invoke actions, copy
// acknowledgments retitle and revert, and each Refresh tick re-probes the
// embedded server to keep the status line honest. It is the only goroutine
// touching the items after startup, so no locking is needed.
func (m *Menu) Loop(done <-chan struct{}, ev Events, status, config Item) {
	after := m.After
	if after == nil {
		after = time.After
	}
	ackFor := m.AckDuration
	if ackFor == 0 {
		ackFor = DefaultAckDuration
	}

	healthy := m.Actions.Probe()
	var statusAck, configAck <-chan time.Time
	for {
		select {
		case <-done:
			return
		case <-ev.ViewClicked:
			m.Actions.ShowWindow()
		case <-ev.PairClicked:
			m.Actions.OpenPairing()
		case <-ev.StatusClicked:
			if m.Actions.CopyText(m.Endpoint) {
				status.SetTitle(StatusCopiedTitle())
				statusAck = after(ackFor)
			}
		case <-ev.ConfigClicked:
			if m.Actions.CopyText(m.ConfigJSON) {
				config.SetTitle(ConfigCopiedTitle())
				configAck = after(ackFor)
			}
		case <-statusAck:
			statusAck = nil
			status.SetTitle(StatusTitle(m.Endpoint, healthy))
		case <-configAck:
			configAck = nil
			config.SetTitle(CopyConfigTitle)
		case <-ev.Refresh:
			healthy = m.Actions.Probe()
			if statusAck == nil { // never clobber a visible acknowledgment
				status.SetTitle(StatusTitle(m.Endpoint, healthy))
			}
		case <-ev.QuitClicked:
			m.Actions.Quit()
		}
	}
}
