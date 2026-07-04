// Governing: ADR-0018 (pairing/trust surface), SPEC-0011 REQ "Error Handling
// Standards" — sentinel errors for every failure mode callers distinguish
// programmatically, structured context carried alongside, never swallowed.
package devices

import (
	"errors"
	"fmt"
)

// Sentinel errors for the pairing and trust layer. Callers match these with
// errors.Is; layers above wrap them with contextual detail (peer, endpoint,
// path) rather than returning them bare.
var (
	// ErrTokenExpired is returned when a pairing token is presented after its
	// TTL has elapsed on the issuing node's clock. The window closes.
	ErrTokenExpired = errors.New("devices: pairing token expired")

	// ErrTokenConsumed is returned when a pairing token is presented again
	// after a pairing already completed with it (replay).
	ErrTokenConsumed = errors.New("devices: pairing token already consumed")

	// ErrTokenInvalid is returned when a presented token does not match the
	// window's token. Five consecutive mismatches close the window.
	ErrTokenInvalid = errors.New("devices: pairing token invalid")

	// ErrWindowClosed is returned when a token is presented and no pairing
	// window is open (explicitly closed, rate-limit closed, or device sync
	// disabled).
	ErrWindowClosed = errors.New("devices: pairing window closed")

	// ErrUnknownPeerCertificate is returned when a connection presents a TLS
	// certificate whose fingerprint is not pinned in the peer registry.
	ErrUnknownPeerCertificate = errors.New("devices: unknown peer certificate")

	// ErrFingerprintMismatch is returned when the certificate presented during
	// pairing does not match the fingerprint carried by the QR/manual payload
	// (possible wrong device or man-in-the-middle). The replica aborts before
	// the token is ever transmitted.
	ErrFingerprintMismatch = errors.New("devices: certificate fingerprint mismatch")

	// ErrHashMismatch is returned when a fetched archive file fails SHA-256
	// verification against its manifest entry. See HashMismatchError for the
	// attributable form the transfer engine logs.
	ErrHashMismatch = errors.New("devices: file hash mismatch")

	// ErrImporterConflict is returned when a peer attempts to register as
	// importer for a source that already has a different registered importer.
	ErrImporterConflict = errors.New("devices: importer already registered for source")

	// ErrRedirectResponse is returned when a peer answers a sync request with
	// any 3xx. The sync protocol never emits redirects (SPEC-0011 "Redirect
	// Validation"), so a redirect is a protocol violation and the client
	// aborts instead of following it.
	ErrRedirectResponse = errors.New("devices: peer emitted a redirect (sync protocol violation)")
)

// HashMismatchError is the attributable form of ErrHashMismatch: it carries
// everything SPEC-0011's "Hash mismatch is attributable" scenario requires a
// log entry to hold — source, relative path, expected and actual hashes, and
// peer identity. errors.Is(err, ErrHashMismatch) matches it.
type HashMismatchError struct {
	Source   string // archive source (e.g. "signal")
	Path     string // manifest-relative file path
	Expected string // manifest SHA-256, lowercase hex
	Actual   string // computed SHA-256, lowercase hex
	Peer     string // peer device name or fingerprint
}

// Error renders the mismatch with full attribution.
func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("devices: hash mismatch fetching %s/%s from %s: expected %s, got %s",
		e.Source, e.Path, e.Peer, e.Expected, e.Actual)
}

// Is reports true for ErrHashMismatch so callers can match the sentinel
// without knowing the concrete type.
func (e *HashMismatchError) Is(target error) bool { return target == ErrHashMismatch }
