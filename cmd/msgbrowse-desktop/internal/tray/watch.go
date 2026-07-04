// Tray-registration watchdog (issue #167). fyne.io/systray reports no error
// from registration — on macOS a status item that never renders is simply
// silent, which is exactly how the #122 menubar shipped broken on real
// hardware while headless CI stayed green. The shell instead treats the
// library's onReady callback as the registration signal and races it against
// a deadline: ready in time is Ready, a missed deadline is Timeout (logged
// AND surfaced on the Logs page), and app shutdown before either is
// Cancelled (not a failure — the user quit first).
//
// Pure channel logic, headlessly testable with CGO_ENABLED=0; the command
// root supplies the real channels (systray onReady, time.After, ctx.Done).
package tray

// Readiness is the watchdog's verdict on status-item registration.
type Readiness int

const (
	// Ready: onReady fired — the status item registered and the quick menu
	// was installed.
	Ready Readiness = iota
	// Timeout: onReady did not fire before the deadline — the tray is
	// treated as unavailable and the failure must be surfaced (issue #167).
	Timeout
	// Cancelled: the app began shutting down before either outcome.
	Cancelled
)

// AwaitReady blocks until the tray's onReady fires (Ready), deadline
// delivers (Timeout), or cancel closes (Cancelled). ready is level-triggered
// (closed by the tray's onReady), so a Ready verdict is never lost to
// channel-select ordering.
func AwaitReady(ready <-chan struct{}, deadline <-chan struct{}, cancel <-chan struct{}) Readiness {
	select {
	case <-ready:
		return Ready
	case <-deadline:
		// Don't let a lost race misreport a tray that did come up.
		select {
		case <-ready:
			return Ready
		default:
		}
		return Timeout
	case <-cancel:
		return Cancelled
	}
}
