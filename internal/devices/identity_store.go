// Governing: ADR-0018 / SPEC-0011 REQ "Pairing Acceptance and Mutual
// Certificate Pinning" — "each node MUST generate a long-lived self-signed
// TLS keypair when device sync is first enabled". This file is that
// enablement point: identity PEMs persisted under data_dir (never the
// archive), private key mode 0600, loaded verbatim on every later start so
// the fingerprint peers pinned stays stable for the certificate's lifetime.
package devices

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Identity PEM filenames within the device-sync state directory.
const (
	// IdentityCertFile holds the self-signed certificate (public; its
	// SHA-256 fingerprint is what peers pin).
	IdentityCertFile = "identity.crt"
	// IdentityKeyFile holds the PKCS#8 private key. It never leaves the
	// node and is written 0600.
	IdentityKeyFile = "identity.key"
)

// IdentityDir returns the directory device-sync identity material lives in:
// <data_dir>/devices. Inside data_dir by design — writable node-local state,
// excluded from manifests and sync (SPEC-0011 "Database Is Never
// Transferred" covers all of data_dir).
func IdentityDir(dataDir string) string {
	return filepath.Join(dataDir, "devices")
}

// LoadIdentityFromDir loads a persisted identity from dir. A missing
// identity (either PEM absent) is reported as fs.ErrNotExist so callers can
// distinguish "not enabled yet" from corruption.
func LoadIdentityFromDir(dir string) (*Identity, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, IdentityCertFile))
	if err != nil {
		return nil, fmt.Errorf("devices: read identity certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, IdentityKeyFile))
	if err != nil {
		return nil, fmt.Errorf("devices: read identity key: %w", err)
	}
	id, err := LoadIdentity(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("devices: identity in %s: %w", dir, err)
	}
	return id, nil
}

// LoadOrCreateIdentity loads the node identity from dir, generating and
// persisting a fresh one (DefaultCertLifetime) when none exists yet. created
// reports whether a new identity was minted this call — callers log that
// loudly, since it is the moment the pinnable fingerprint is born. The key
// is written 0600 and dir is created 0700.
func LoadOrCreateIdentity(dir, deviceName string) (id *Identity, created bool, err error) {
	id, err = LoadIdentityFromDir(dir)
	switch {
	case err == nil:
		return id, false, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, false, err
	}

	id, err = NewIdentity(deviceName, 0)
	if err != nil {
		return nil, false, err
	}
	certPEM, keyPEM, err := id.EncodePEM()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, fmt.Errorf("devices: create identity dir %s: %w", dir, err)
	}
	// Key first: if the cert write fails the next call regenerates both; a
	// cert without its key would load as a broken identity.
	if err := os.WriteFile(filepath.Join(dir, IdentityKeyFile), keyPEM, 0o600); err != nil {
		return nil, false, fmt.Errorf("devices: write identity key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, IdentityCertFile), certPEM, 0o600); err != nil {
		return nil, false, fmt.Errorf("devices: write identity certificate: %w", err)
	}
	return id, true, nil
}
