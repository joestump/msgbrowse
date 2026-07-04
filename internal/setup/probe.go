// Governing: ADR-0020 (OS consent gates are DETECT-AND-GUIDE only — the app
// detects a missing grant and never bypasses OS consent), SPEC-0013 REQ
// "Permission detection and guidance".
package setup

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/joestump/msgbrowse/internal/source"
)

// PermissionState is the outcome of an OS-permission probe. The probes are
// detect-only: they report whether a grant appears present, missing, or simply
// not applicable to this OS — they never attempt to acquire, bypass, or spoof a
// grant (ADR-0020 decision 2).
type PermissionState int

const (
	// PermissionOK: the grant is present / the resource is accessible.
	PermissionOK PermissionState = iota
	// PermissionNeeded: the resource exists but the process cannot access it —
	// the OS consent grant is missing. This is the state that drives the
	// Needs-permission card and the System Settings deep link (#134).
	PermissionNeeded
	// PermissionNotApplicable: the probe does not apply on this OS or the
	// resource simply is not present (e.g. no chat.db, no Signal config) — there
	// is nothing to grant. Distinct from Needed so the UI shows a Not-detected
	// source rather than a permission prompt.
	PermissionNotApplicable
)

// String renders a stable token for logs and tests.
func (p PermissionState) String() string {
	switch p {
	case PermissionOK:
		return "ok"
	case PermissionNeeded:
		return "needs-permission"
	default:
		return "n/a"
	}
}

// PermissionProbe is one source's permission-probe result: which source, the
// state, and the resource that was probed (so guidance can name it).
type PermissionProbe struct {
	// Source is the fixed source id the probe concerns.
	Source string
	// State is OK / Needed / NotApplicable.
	State PermissionState
	// Resource is the concrete path (or artifact) the probe examined, for the
	// UI to reference in its guidance.
	Resource string
}

// opener abstracts "can I open this file for reading?" so Full Disk Access can
// be probed against a faked tree in tests. os.Open is the production impl.
type opener func(string) (io.ReadCloser, error)

func osOpen(path string) (io.ReadCloser, error) { return os.Open(path) }

func (d Detector) open() opener {
	if d.Open != nil {
		return d.Open
	}
	return osOpen
}

// ProbeFullDiskAccess probes whether the process can actually READ the iMessage
// chat.db — the concrete signal of macOS Full Disk Access. The distinction the
// probe draws (SPEC-0013 REQ "iMessage enabled without Full Disk Access"):
//
//   - the file does not exist          → NotApplicable (nothing to grant; off
//     macOS the path never exists, so this is the universal non-macOS answer)
//   - the file exists but Open fails    → Needed (FDA not granted — the classic
//     macOS "chat.db is there but sandboxed" state)
//   - the file exists and opens         → OK
//
// The core is testable on Linux: point Home at a faked tree and inject Open to
// return a permission error for the chat.db to exercise the Needed branch.
func (d Detector) ProbeFullDiskAccess() PermissionProbe {
	path := d.path(imessageRel)
	probe := PermissionProbe{Source: source.IMessage, Resource: path}
	if d.fileState(path) != Detected {
		probe.State = PermissionNotApplicable
		return probe
	}
	f, err := d.open()(path)
	if err != nil {
		probe.State = PermissionNeeded
		return probe
	}
	_ = f.Close()
	probe.State = PermissionOK
	return probe
}

// signalConfig is the shape of Signal Desktop's config.json we care about: the
// presence of encryptedKey signals that the message DB key is sealed behind the
// OS keychain (SPEC-0013 "Signal Desktop Keychain access"). Only this field is
// read; the rest of the file is ignored.
type signalConfig struct {
	EncryptedKey string `json:"encryptedKey"`
}

// signalConfigRel is Signal Desktop's config file, relative to its
// application-support directory.
const signalConfigRel = "config.json"

// ProbeSignalKeychain probes Signal Desktop's key accessibility. The real
// macOS Keychain "Always Allow" grant can only be verified on macOS; this
// provides the testable core the desktop layer (#134) wraps with the actual
// Keychain call. It reports:
//
//   - no Signal config.json                       → NotApplicable (Signal not
//     installed / nothing to grant; also the non-macOS answer)
//   - config.json present but no encryptedKey      → OK (an unencrypted/legacy
//     key needs no keychain grant)
//   - config.json with an encryptedKey             → the key is sealed; whether
//     the OS will release it is decided by KeychainAccessible (injected), which
//     defaults to "cannot verify off macOS" → Needed on this Linux box, and is
//     overridden by the desktop layer with the real macOS check.
//
// KeychainAccessible lets tests drive both the OK and Needed branches
// deterministically without a real keychain.
func (d Detector) ProbeSignalKeychain() PermissionProbe {
	dir := d.path(signalRel)
	cfgPath := ""
	if dir != "" {
		cfgPath = filepath.Join(dir, signalConfigRel)
	}
	probe := PermissionProbe{Source: source.Signal, Resource: cfgPath}
	if cfgPath == "" {
		probe.State = PermissionNotApplicable
		return probe
	}
	f, err := d.open()(cfgPath)
	if err != nil {
		// No config.json (or unreadable): nothing to unseal.
		probe.State = PermissionNotApplicable
		return probe
	}
	defer f.Close()
	var cfg signalConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil || cfg.EncryptedKey == "" {
		// Unparseable or no encrypted key: no keychain grant is required.
		probe.State = PermissionOK
		return probe
	}
	// The key is sealed. Whether the OS keychain will release it is the injected
	// check; off macOS it cannot be verified, so the default reports Needed and
	// the UI guides the user (rather than silently claiming access).
	if d.keychainAccessible()(cfg.EncryptedKey) {
		probe.State = PermissionOK
	} else {
		probe.State = PermissionNeeded
	}
	return probe
}

func (d Detector) keychainAccessible() func(string) bool {
	if d.KeychainAccessible != nil {
		return d.KeychainAccessible
	}
	// Default: the real macOS Keychain check is only meaningful on macOS. Here
	// (and anywhere the desktop layer does not inject a real check) a sealed key
	// is reported as Needed so the UI guides the user rather than assuming
	// access. The desktop shell (#134) injects the genuine macOS check.
	return func(string) bool { return false }
}

// ProbeWhatsAppContainer probes whether the process can read the WhatsApp app's
// ChatStorage.sqlite inside its group container — the WhatsApp-container access
// gate. Same three-way logic as Full Disk Access:
//
//   - no container database        → NotApplicable (WhatsApp app not present)
//   - database present but Open fails → Needed (container access not granted)
//   - database present and opens    → OK
func (d Detector) ProbeWhatsAppContainer() PermissionProbe {
	det := d.DetectWhatsApp()
	probe := PermissionProbe{Source: source.WhatsApp, Resource: det.Path}
	if det.State != Detected {
		probe.State = PermissionNotApplicable
		return probe
	}
	f, err := d.open()(det.Path)
	if err != nil {
		probe.State = PermissionNeeded
		return probe
	}
	_ = f.Close()
	probe.State = PermissionOK
	return probe
}
