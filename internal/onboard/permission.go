// Permission-shaped export-failure classification (issue #174). A macOS TCC
// grant (Full Disk Access) is bound to the app's code signature, so replacing
// an ad-hoc-signed .app silently invalidates a grant that still shows enabled
// in System Settings — the pre-flight probe can pass or be absent, and the
// exporter itself then dies on the permission wall. The runner classifies that
// failure from the exporter's captured output so the terminal error wraps
// ErrPermissionDenied and the UI re-enters the detect-and-guide flow (ADR-0020)
// instead of resting on a generic Failed.
//
// Classification runs ONLY on a non-zero exit — a successful export is never
// reclassified, no matter what its output chattered about.
//
// Governing: SPEC-0013 REQ "Permission detection and guidance" (a missing grant
// is surfaced as guidance, never a silent or generic failure), REQ "Error
// Handling Standards" (the failing step and a human-readable message, never
// reduced to a generic failure).
package onboard

import "strings"

// permissionPatterns are the case-insensitive substrings that mark an
// exporter's captured stderr/stdout as an OS-permission failure. Each entry is
// a real shape seen (or expected) from the exporters hitting a TCC gate; a new
// shape is a one-line addition here:
//
//   - "full disk access": imessage-exporter's explicit FDA hint ("Ensure full
//     disk access is enabled for your terminal emulator in System Settings…").
//   - "unable to open database file": SQLite's error when TCC blocks the open
//     (the chat.db / ChatStorage.sqlite read behind FDA).
//   - "operation not permitted": the raw EPERM text macOS returns for a
//     sandbox/TCC-blocked filesystem call.
//   - "authorization denied": the macOS authorization-services phrasing some
//     tools surface for a denied consent.
var permissionPatterns = []string{
	"full disk access",
	"unable to open database file",
	"operation not permitted",
	"authorization denied",
}

// permissionShaped reports whether an exporter's captured combined output looks
// like an OS-permission failure. It is a plain case-insensitive substring scan
// over the bounded ring-buffer tail the ExecRunner captured — the tail is where
// a fatal permission error prints, and 32 KiB is far more than these one-line
// diagnostics need.
func permissionShaped(output string) bool {
	lower := strings.ToLower(output)
	for _, pat := range permissionPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// classifyExportFailure picks the sentinel for a non-zero exporter exit from
// its captured output: a permission-shaped output classifies as
// ErrPermissionDenied (the UI re-enters the guidance flow, issue #174);
// anything else stays the generic ErrExportFailed. Callers wrap the concrete
// exit error alongside the returned sentinel (wrapSentinel), so the original
// error is preserved and errors.Is matches the classification.
func classifyExportFailure(output string) error {
	if permissionShaped(output) {
		return ErrPermissionDenied
	}
	return ErrExportFailed
}
