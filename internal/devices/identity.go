// Governing: ADR-0018 (mutual TLS from pairing-pinned self-signed
// certificates), SPEC-0011 REQ "Pairing Acceptance and Mutual Certificate
// Pinning" — long-lived self-signed keypair, fingerprints verified against
// the pairing payload (never the WebPKI), TLS 1.3 minimum, exact
// pinned-fingerprint match both directions, unknown certificates rejected at
// the TLS layer. Standard library crypto only (ADR-0013: CGO_ENABLED=0, no
// new dependencies).
package devices

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// DefaultCertLifetime is the validity of a freshly generated device identity.
// Long-lived by design (ADR-0018): trust comes from pinning, not expiry;
// `doctor` warns ahead of expiry and rotation is re-pairing in v1.
const DefaultCertLifetime = 10 * 365 * 24 * time.Hour

// Identity is a node's long-lived self-signed TLS identity: one Ed25519
// keypair and certificate used for both server and client roles (the same
// device dials and answers), plus its canonical SHA-256 fingerprint.
type Identity struct {
	// TLSCertificate is ready to place in a tls.Config.Certificates slice.
	TLSCertificate tls.Certificate
	// Leaf is the parsed certificate.
	Leaf *x509.Certificate
	// fingerprint is the canonical lowercase-hex SHA-256 of Leaf.Raw.
	fingerprint string
}

// NewIdentity generates a fresh self-signed Ed25519 identity for deviceName,
// valid from now (with an hour of backdating for peer clock skew) for
// lifetime (DefaultCertLifetime when 0). Called once, when device sync is
// first enabled on a node; persist with EncodePEM and reload with
// LoadIdentity.
func NewIdentity(deviceName string, lifetime time.Duration) (*Identity, error) {
	if deviceName == "" {
		return nil, fmt.Errorf("devices: identity device name must not be empty")
	}
	if lifetime == 0 {
		lifetime = DefaultCertLifetime
	}
	if lifetime < 0 {
		return nil, fmt.Errorf("devices: identity lifetime %v is negative", lifetime)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devices: generate identity key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("devices: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: deviceName},
		NotBefore:    now.Add(-time.Hour), // tolerate modest peer clock skew
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, // answers sync connections
			x509.ExtKeyUsageClientAuth, // dials peers with the same identity
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("devices: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("devices: parse generated certificate: %w", err)
	}
	return &Identity{
		TLSCertificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf},
		Leaf:           leaf,
		fingerprint:    Fingerprint(der),
	}, nil
}

// Fingerprint computes the canonical fingerprint of a DER-encoded
// certificate: lowercase hex SHA-256 of the full certificate bytes. This is
// the exact string that travels in the pairing payload's "fp" field and is
// persisted in the paired_devices registry — always 64 characters, no colons.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns the identity's canonical certificate fingerprint.
func (id *Identity) Fingerprint() string { return id.fingerprint }

// NormalizeFingerprint canonicalizes a user- or peer-supplied fingerprint:
// lowercased, colons and spaces stripped (accepting the common
// AA:BB:… display form). Returns an error unless the result is exactly 64
// hex characters.
func NormalizeFingerprint(fp string) (string, error) {
	cleaned := strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(strings.TrimSpace(fp)))
	if len(cleaned) != sha256.Size*2 {
		return "", fmt.Errorf("devices: fingerprint must be %d hex chars, got %d", sha256.Size*2, len(cleaned))
	}
	if _, err := hex.DecodeString(cleaned); err != nil {
		return "", fmt.Errorf("devices: fingerprint is not hex: %w", err)
	}
	return cleaned, nil
}

// EncodePEM serializes the identity as two PEM blocks (CERTIFICATE +
// PRIVATE KEY in PKCS#8) for persistence under data_dir. The private key
// never leaves the node; only the certificate's fingerprint is shared.
func (id *Identity) EncodePEM() (certPEM, keyPEM []byte, err error) {
	keyDER, err := x509.MarshalPKCS8PrivateKey(id.TLSCertificate.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("devices: marshal private key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Leaf.Raw})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// LoadIdentity reconstructs an Identity from EncodePEM output.
func LoadIdentity(certPEM, keyPEM []byte) (*Identity, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("devices: load identity keypair: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("devices: parse identity certificate: %w", err)
	}
	cert.Leaf = leaf
	return &Identity{
		TLSCertificate: cert,
		Leaf:           leaf,
		fingerprint:    Fingerprint(leaf.Raw),
	}, nil
}

// verifyPinnedPeer returns a tls.Config VerifyPeerCertificate callback that
// accepts exactly one certificate fingerprint. It sees the raw DER the peer
// presented (chain verification is disabled — pinning replaces the WebPKI
// entirely per SPEC-0011), so a mismatch fails the handshake itself: nothing
// application-level, token included, is ever written to a mismatched peer.
func verifyPinnedPeer(pinned string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("%w: peer presented no certificate", ErrUnknownPeerCertificate)
		}
		got := Fingerprint(rawCerts[0])
		if got != pinned {
			return fmt.Errorf("%w: presented %s, pinned %s", ErrFingerprintMismatch, got, pinned)
		}
		return nil
	}
}

// ClientTLS builds the tls.Config a node uses to dial a peer whose
// certificate fingerprint it knows — from the pairing payload before trust
// exists, or from the paired_devices registry afterwards. The chain
// verification the WebPKI would do is replaced wholesale by an exact
// fingerprint match (InsecureSkipVerify only disables the WebPKI path;
// VerifyPeerCertificate still runs and is the sole authority). A mismatch
// aborts the handshake before any application byte — in pairing, before the
// token is transmitted (SPEC-0011 "Fingerprint mismatch aborts before token
// disclosure").
func (id *Identity) ClientTLS(pinnedFingerprint string) (*tls.Config, error) {
	fp, err := NormalizeFingerprint(pinnedFingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{id.TLSCertificate},
		InsecureSkipVerify:    true, // WebPKI replaced by exact pinning below
		VerifyPeerCertificate: verifyPinnedPeer(fp),
	}, nil
}

// NewPeerClient builds the http.Client a node uses to call a peer whose
// certificate fingerprint it knows: transport pinned via ClientTLS (so a
// fingerprint mismatch aborts in the handshake, before any application byte),
// and redirects refused outright — the sync API never emits them, so any 3xx
// is a protocol violation the client aborts on (SPEC-0011 "Redirect
// Validation"). timeout of 0 means no client timeout (callers bound requests
// with contexts instead).
func (id *Identity) NewPeerClient(pinnedFingerprint string, timeout time.Duration) (*http.Client, error) {
	cfg, err := id.ClientTLS(pinnedFingerprint)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: cfg},
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("devices: refusing redirect to %s: %w", req.URL, ErrRedirectResponse)
		},
	}, nil
}

// PairingServerTLS builds the tls.Config for the importer's pairing
// endpoint: it presents the node certificate (whose fingerprint the QR
// payload carries) and requires the connecting replica to present a client
// certificate so a successful token exchange can pin it. The client
// certificate is NOT verified against anything at handshake time — before
// pairing completes there is nothing to pin against; the single-use token is
// the gate (SPEC-0011 Authentication table, POST /v1/pair).
func (id *Identity) PairingServerTLS() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.TLSCertificate},
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

// PinnedServerTLS builds the tls.Config for every non-pairing sync endpoint:
// mutual TLS where the client must present a certificate whose fingerprint
// isPinned recognizes (backed by the paired_devices registry). Unknown
// certificates are rejected at the TLS layer with ErrUnknownPeerCertificate
// carrying the presented fingerprint, so the rejection is loggable per
// SPEC-0011 "Unknown certificate rejected after pairing".
func (id *Identity) PinnedServerTLS(isPinned func(fingerprint string) bool) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.TLSCertificate},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("%w: peer presented no certificate", ErrUnknownPeerCertificate)
			}
			fp := Fingerprint(rawCerts[0])
			if !isPinned(fp) {
				return fmt.Errorf("%w: presented fingerprint %s", ErrUnknownPeerCertificate, fp)
			}
			return nil
		},
	}
}
