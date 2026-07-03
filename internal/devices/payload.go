// Governing: ADR-0018 (this ADR owns the pairing payload), SPEC-0011 REQ
// "Pairing Initiation" — the payload {protocol version, listener endpoint,
// token, SHA-256 fingerprint of the listener's TLS certificate} presented as
// both QR bytes and a copyable manual code carrying the same fields.
// SPEC-0010's Connect/settings page renders this payload; this package
// defines it.
package devices

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// PayloadVersion is the current pairing payload protocol version. A decoder
// rejects any other value: the payload is a one-shot secret scanned between
// two builds the same operator controls, so there is no compatibility window
// to honor — a version bump means "re-open the pairing window on matching
// builds".
const PayloadVersion = 1

// ManualCodePrefix prefixes the copyable manual pairing code so a pasted
// string is self-identifying ("MSGB1." + base64url payload JSON, no padding).
const ManualCodePrefix = "MSGB1."

// PairingPayload is the exact wire schema of the pairing payload — the single
// source of truth consumed by SPEC-0010's Connect page (QR rendering) and by
// replicas (scan / paste). Version 1 is compact JSON with these fields and no
// others:
//
//	{
//	  "v":        1,                    // PayloadVersion (integer, required)
//	  "endpoint": "192.168.1.10:8788",  // importer sync listener host:port (required)
//	  "token":    "dGhpcy1pcy1ub3Q…",   // single-use pairing token, 43-char
//	                                    // base64url (RawURLEncoding) of 32
//	                                    // random bytes (required, secret)
//	  "fp":       "9f86d081884c7d65…"   // SHA-256 fingerprint of the listener's
//	                                    // TLS certificate DER: exactly 64
//	                                    // lowercase hex chars, no colons (required)
//	}
//
// Two presentations, identical fields (SPEC-0011 REQ "Pairing Initiation"):
//
//   - QR bytes: the compact JSON encoding itself (EncodeQR) — small enough
//     (~170 bytes) for a comfortably scannable QR code.
//   - Manual code: "MSGB1." + base64url(compact JSON), no padding
//     (EncodeManualCode) — copy/paste-safe: one token, no whitespace, no
//     shell-hostile characters.
//
// The payload carries a live pairing secret. It is displayed only on the
// loopback web UI, never logged, and dies with the window (token TTL ≤ 10
// minutes, single use).
type PairingPayload struct {
	// Version is the payload protocol version; always PayloadVersion.
	Version int `json:"v"`
	// Endpoint is the importer's sync listener as host:port. The replica
	// pairs against it and persists it as the peer's last-known address.
	Endpoint string `json:"endpoint"`
	// Token is the single-use pairing window token (Window.Token).
	Token string `json:"token"`
	// Fingerprint is the importer certificate's canonical SHA-256 fingerprint
	// (see Fingerprint): the replica verifies the pairing TLS handshake
	// against it — never the WebPKI — before the token is transmitted.
	Fingerprint string `json:"fp"`
}

// NewPairingPayload assembles and validates the payload for an open window.
func NewPairingPayload(endpoint, token, fingerprint string) (*PairingPayload, error) {
	p := &PairingPayload{
		Version:     PayloadVersion,
		Endpoint:    endpoint,
		Token:       token,
		Fingerprint: fingerprint,
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	fp, err := NormalizeFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	p.Fingerprint = fp
	return p, nil
}

// validate checks the invariants every encode and decode path enforces.
func (p *PairingPayload) validate() error {
	if p.Version != PayloadVersion {
		return fmt.Errorf("devices: unsupported pairing payload version %d (want %d)", p.Version, PayloadVersion)
	}
	if _, _, err := net.SplitHostPort(p.Endpoint); err != nil {
		return fmt.Errorf("devices: pairing payload endpoint %q is not host:port: %w", p.Endpoint, err)
	}
	if p.Token == "" {
		return fmt.Errorf("devices: pairing payload token must not be empty")
	}
	if _, err := NormalizeFingerprint(p.Fingerprint); err != nil {
		return fmt.Errorf("devices: pairing payload fingerprint: %w", err)
	}
	return nil
}

// EncodeQR returns the bytes a QR renderer encodes: the payload's compact
// JSON. The QR image itself is generated locally by the settings page
// (SPEC-0011 Security Headers: no external QR service, `img-src 'self'
// data:`).
func (p *PairingPayload) EncodeQR() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// EncodeManualCode returns the copyable manual pairing code carrying exactly
// the same fields as the QR: ManualCodePrefix + base64url (no padding) of the
// compact JSON.
func (p *PairingPayload) EncodeManualCode() (string, error) {
	raw, err := p.EncodeQR()
	if err != nil {
		return "", err
	}
	return ManualCodePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodePayload parses payload bytes in either presentation: raw compact JSON
// (a decoded QR) or the "MSGB1."-prefixed manual code (whitespace-tolerant,
// as pasted text tends to pick up a stray newline). It validates every field,
// including the version gate and fingerprint canonicalization, so callers
// downstream can trust the shape.
func DecodePayload(data []byte) (*PairingPayload, error) {
	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, ManualCodePrefix) {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, ManualCodePrefix))
		if err != nil {
			return nil, fmt.Errorf("devices: decode manual pairing code: %w", err)
		}
		s = string(raw)
	}
	var p PairingPayload
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("devices: decode pairing payload: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	fp, err := NormalizeFingerprint(p.Fingerprint)
	if err != nil {
		return nil, err
	}
	p.Fingerprint = fp
	return &p, nil
}
