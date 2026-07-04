// Headless tests for the desktop Keychain-check injection (#134). These run with
// CGO_ENABLED=0 on Linux CI: the real macOS `security` probe is inert off macOS
// (runtime.GOOS != "darwin"), so the injected check reports "cannot verify",
// which the core turns into Needs-permission — exactly the behavior the shipped
// .app must fall back to, and what these assert on a non-macOS box.
package embedded

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/joestump/msgbrowse/internal/setup"
)

// TestNewDesktopDetectorInjectsKeychainCheck asserts the desktop detector wires a
// non-nil KeychainAccessible seam — the whole point of #134's desktop injection.
// The core defaults it to nil (→ its own "cannot verify" closure); the desktop
// layer must supply the genuine check instead.
func TestNewDesktopDetectorInjectsKeychainCheck(t *testing.T) {
	d := newDesktopDetector()
	if d.KeychainAccessible == nil {
		t.Fatal("desktop detector must inject a KeychainAccessible check (nil means the real macOS probe was not wired)")
	}
}

// TestMacOSKeychainAccessibleOffDarwin: off macOS the injected check must report
// false ("cannot verify"), so a sealed Signal key reads as Needs-permission and
// the UI guides the user rather than falsely claiming access. This is the branch
// that runs on Linux CI.
func TestMacOSKeychainAccessibleOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this asserts the non-macOS fallback; on darwin the real Keychain is probed")
	}
	if macOSKeychainAccessible("deadbeef") {
		t.Error("off macOS the keychain check must report false (cannot verify)")
	}
}

// TestDesktopDetectorSealedKeyNeedsPermissionOffDarwin drives the end-to-end
// probe through the injected detector on a faked HOME with a sealed Signal key:
// off macOS the injected check reports false, so ProbeSignalKeychain returns
// Needs-permission — the guided-setup state that shows the Keychain "Always
// Allow" guidance.
func TestDesktopDetectorSealedKeyNeedsPermissionOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-macOS fallback assertion")
	}
	home := t.TempDir()
	sigDir := filepath.Join(home, "Library", "Application Support", "Signal")
	if err := os.MkdirAll(sigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sigDir, "config.json"),
		[]byte(`{"encryptedKey":"deadbeef"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	d := newDesktopDetector()
	d.Home = home // re-root the real-filesystem detector at the faked HOME
	if got := d.ProbeSignalKeychain(); got.State != setup.PermissionNeeded {
		t.Errorf("sealed key with the desktop (off-macOS) keychain check = %v, want PermissionNeeded", got.State)
	}
}
