// Wiring the REAL macOS Keychain accessibility check into the desktop Setup
// permission probe (SPEC-0013 REQ "Permission detection and guidance", #134).
//
// internal/setup.ProbeSignalKeychain leaves the "will the OS keychain release
// Signal's sealed key?" decision as an injectable seam (Detector.KeychainAccessible),
// defaulting to "cannot verify" off macOS so a sealed key reads as
// Needs-permission on the pure-Go, Linux-tested core. Here — in the desktop
// embedded server — we inject the genuine macOS check so the shipped .app probes
// the actual Keychain, while the core stays pure-Go and Linux-testable.
//
// The check is DETECT-ONLY (ADR-0020 decision 2): it asks the OS whether access
// is already granted; it never prompts, bypasses, or stores the key. It shells
// out to the macOS `security` tool (no cgo), so the whole embedded package stays
// CGO_ENABLED=0-buildable and its headless suite still runs on Linux; the check
// is gated on runtime.GOOS == "darwin" so it is inert (and the injected seam
// simply reports "cannot verify") on any non-macOS build.
//
// Governing: ADR-0020 (OS consent gates are detect-and-guide only; the desktop
// app injects the genuine macOS check), SPEC-0013 REQ "Permission detection and
// guidance".
package embedded

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"github.com/joestump/msgbrowse/internal/setup"
)

// signalKeychainService / signalKeychainAccount identify Signal Desktop's
// generic-password Keychain item — the sealed "safe storage" key macOS gates
// with the "Always Allow" grant. These are Signal Desktop's fixed Electron
// safeStorage identifiers on macOS.
const (
	signalKeychainService = "Signal Safe Storage"
	signalKeychainAccount = "Signal"
)

// keychainProbeTimeout bounds the `security` invocation so a wedged Keychain
// daemon reads as "not accessible" (→ Needs-permission guidance) rather than
// hanging the Setup probe.
const keychainProbeTimeout = 3 * time.Second

// newDesktopDetector builds the source Detector the desktop Setup surface uses:
// a HOME-rooted real-filesystem detector (like setup.NewDetector) with the
// genuine macOS Keychain accessibility check injected. On non-macOS builds the
// injected check reports "cannot verify" (identical to the core default), so a
// dev/Linux desktop run behaves exactly like the untouched core.
func newDesktopDetector() setup.Detector {
	d := setup.NewDetector()
	d.KeychainAccessible = macOSKeychainAccessible
	return d
}

// macOSKeychainAccessible reports whether the macOS Keychain will release
// Signal Desktop's sealed key WITHOUT prompting — the concrete signal of the
// "Always Allow" grant. Off macOS it always reports false ("cannot verify"), so
// the core's Needs-permission default is preserved and the UI guides the user.
//
// On macOS it runs `security find-generic-password -w` for Signal's item: a
// zero exit (the secret is printed to stdout, which we discard) means the
// calling app is already trusted for that item — access is granted. A non-zero
// exit (item not found, or access would require a prompt/was denied) reads as
// not-yet-accessible, so Setup shows the Keychain "Always Allow" guidance. The
// probe never triggers the interactive prompt: `security` returns non-zero for a
// non-interactive denied item rather than blocking on a dialog.
func macOSKeychainAccessible(_ string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), keychainProbeTimeout)
	defer cancel()
	// -s service, -a account, -w print only the password (discarded). We only
	// care about the exit status: success == already-trusted.
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", signalKeychainService, "-a", signalKeychainAccount, "-w")
	return cmd.Run() == nil
}
