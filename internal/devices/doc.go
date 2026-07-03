// Package devices is the pairing and trust core for msgbrowse's multi-device
// archive synchronization: single-use pairing tokens with a bounded TTL,
// long-lived self-signed TLS identities with SHA-256 fingerprint pinning, the
// versioned QR/manual pairing payload, and the transport-agnostic pairing
// exchange (importer handler + replica client) that later stories mount on a
// real LAN listener.
//
// This package deliberately contains NO network listener. Every primitive is
// exercised over in-memory transports (net.Pipe TLS conns, custom
// http.Transport dialers) so the trust machinery is proven before the first
// socket beyond loopback ever opens. It uses only the standard library's
// crypto/tls and crypto/x509 — no new dependencies, CGO_ENABLED=0 preserved
// (ADR-0013).
//
// # Naming
//
// The `sync` verb belongs to ADR-0015's export→import pipeline
// (`msgbrowse sync`), so this feature adopts the **devices** namespace on
// every surface (resolving SPEC-0011 design.md's naming open question):
//
//   - Go package:  internal/devices (this package)
//   - Config:      the `device_sync` block (internal/config)
//   - CLI:         `msgbrowse devices pair|list|unpair|status` (later stories)
//   - Web routes:  `/settings/devices/...` (later stories)
//   - Schema:      `paired_devices` + `sync_state` tables (internal/store)
//
// Governing: ADR-0018 (multi-device via QR pairing and archive sync),
// SPEC-0011 REQ "Pairing Initiation", REQ "Pairing Acceptance and Mutual
// Certificate Pinning", REQ "Error Handling Standards".
package devices
