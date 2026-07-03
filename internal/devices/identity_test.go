// Governing: SPEC-0011 REQ "Pairing Acceptance and Mutual Certificate
// Pinning" — fingerprint match/mismatch and TLS-layer rejection of unpinned
// certificates, exercised over net.Pipe so no socket is ever opened.
package devices

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustIdentity(t *testing.T, name string) *Identity {
	t.Helper()
	id, err := NewIdentity(name, 0)
	if err != nil {
		t.Fatalf("NewIdentity(%q): %v", name, err)
	}
	return id
}

func TestNewIdentityProperties(t *testing.T) {
	id := mustIdentity(t, "kitchen-server")

	if got := id.Leaf.Subject.CommonName; got != "kitchen-server" {
		t.Errorf("CN = %q, want kitchen-server", got)
	}
	// The same identity serves and dials, so both EKUs must be present.
	var server, client bool
	for _, eku := range id.Leaf.ExtKeyUsage {
		server = server || eku == x509.ExtKeyUsageServerAuth
		client = client || eku == x509.ExtKeyUsageClientAuth
	}
	if !server || !client {
		t.Errorf("ExtKeyUsage server=%v client=%v, want both", server, client)
	}
	// Long-lived by design (ADR-0018).
	if lifetime := id.Leaf.NotAfter.Sub(id.Leaf.NotBefore); lifetime < 9*365*24*time.Hour {
		t.Errorf("certificate lifetime %v, want ~10y", lifetime)
	}
	// The fingerprint is the canonical lowercase-hex SHA-256 of the DER.
	sum := sha256.Sum256(id.Leaf.Raw)
	if want := hex.EncodeToString(sum[:]); id.Fingerprint() != want {
		t.Errorf("Fingerprint() = %s, want %s", id.Fingerprint(), want)
	}
	if len(id.Fingerprint()) != 64 || strings.ToLower(id.Fingerprint()) != id.Fingerprint() {
		t.Errorf("fingerprint %q is not canonical 64-char lowercase hex", id.Fingerprint())
	}

	if _, err := NewIdentity("", 0); err == nil {
		t.Error("NewIdentity with empty name succeeded, want error")
	}
	if _, err := NewIdentity("x", -time.Hour); err == nil {
		t.Error("NewIdentity with negative lifetime succeeded, want error")
	}
}

func TestIdentityPEMRoundTrip(t *testing.T) {
	id := mustIdentity(t, "mac-importer")
	certPEM, keyPEM, err := id.EncodePEM()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint() != id.Fingerprint() {
		t.Errorf("fingerprint changed across PEM round-trip: %s != %s", loaded.Fingerprint(), id.Fingerprint())
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	id := mustIdentity(t, "n")
	canonical := id.Fingerprint()

	colons := make([]string, 0, 32)
	for i := 0; i < len(canonical); i += 2 {
		colons = append(colons, strings.ToUpper(canonical[i:i+2]))
	}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "canonical passes through", in: canonical, want: canonical},
		{name: "uppercase with colons", in: strings.Join(colons, ":"), want: canonical},
		{name: "surrounding whitespace", in: "  " + canonical + "\n", want: canonical},
		{name: "too short", in: canonical[:63], wantErr: true},
		{name: "too long", in: canonical + "0", wantErr: true},
		{name: "not hex", in: strings.Repeat("zz", 32), wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFingerprint(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeFingerprint(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeFingerprint(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeFingerprint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// handshakePair runs a full mutual TLS handshake over an in-memory conn pair
// (see transport_test.go) — no socket — returning both handshake errors.
func handshakePair(t *testing.T, serverCfg, clientCfg *tls.Config) (serverErr, clientErr error) {
	t.Helper()
	cEnd, sEnd := memConnPair()
	defer cEnd.Close()
	defer sEnd.Close()
	// Watchdog: a wedged handshake is unblocked by closing both ends.
	watchdog := time.AfterFunc(10*time.Second, func() {
		cEnd.Close()
		sEnd.Close()
	})
	defer watchdog.Stop()

	done := make(chan error, 1)
	server := tls.Server(sEnd, serverCfg)
	go func() { done <- server.Handshake() }()

	client := tls.Client(cEnd, clientCfg)
	clientErr = client.Handshake()
	serverErr = <-done

	if clientErr == nil && serverErr == nil {
		// Belt and suspenders: the spec demands TLS 1.3 minimum.
		if v := client.ConnectionState().Version; v != tls.VersionTLS13 {
			t.Errorf("negotiated TLS version %x, want TLS 1.3", v)
		}
	}
	return serverErr, clientErr
}

// TestFingerprintPinning is the table-driven match/mismatch matrix for the
// pairing handshake: the replica (client) pins the fingerprint from the
// payload; only the identity with that exact certificate passes.
func TestFingerprintPinning(t *testing.T) {
	importer := mustIdentity(t, "importer")
	imposter := mustIdentity(t, "imposter") // valid cert, wrong identity
	replica := mustIdentity(t, "replica")

	tests := []struct {
		name      string
		server    *Identity
		pinnedFP  string
		wantMatch bool
	}{
		{name: "matching fingerprint handshakes", server: importer, pinnedFP: importer.Fingerprint(), wantMatch: true},
		{name: "mismatched fingerprint aborts", server: imposter, pinnedFP: importer.Fingerprint(), wantMatch: false},
		{name: "display-form fingerprint normalizes and matches", server: importer, pinnedFP: strings.ToUpper(importer.Fingerprint()), wantMatch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientCfg, err := replica.ClientTLS(tt.pinnedFP)
			if err != nil {
				t.Fatal(err)
			}
			_, clientErr := handshakePair(t, tt.server.PairingServerTLS(), clientCfg)
			if tt.wantMatch {
				if clientErr != nil {
					t.Fatalf("handshake failed: %v", clientErr)
				}
				return
			}
			if clientErr == nil {
				t.Fatal("handshake succeeded against a mismatched certificate")
			}
			if !errors.Is(clientErr, ErrFingerprintMismatch) {
				t.Errorf("handshake error = %v, want ErrFingerprintMismatch", clientErr)
			}
		})
	}
}

func TestClientTLSRejectsBadPin(t *testing.T) {
	replica := mustIdentity(t, "replica")
	if _, err := replica.ClientTLS("junk"); err == nil {
		t.Error("ClientTLS accepted a malformed fingerprint")
	}
}

// TestPinnedServerRejectsUnknownCertificate covers the post-pairing posture
// (SPEC-0011 "Unknown certificate rejected after pairing"): on non-pairing
// endpoints the server accepts only pinned client fingerprints, and the
// rejection happens at the TLS layer, before any application byte.
func TestPinnedServerRejectsUnknownCertificate(t *testing.T) {
	importer := mustIdentity(t, "importer")
	pinnedPeer := mustIdentity(t, "paired-replica")
	stranger := mustIdentity(t, "stranger")

	pinset := map[string]bool{pinnedPeer.Fingerprint(): true}
	serverCfg := importer.PinnedServerTLS(func(fp string) bool { return pinset[fp] })

	t.Run("pinned peer accepted", func(t *testing.T) {
		clientCfg, err := pinnedPeer.ClientTLS(importer.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		serverErr, clientErr := handshakePair(t, serverCfg, clientCfg)
		if serverErr != nil || clientErr != nil {
			t.Fatalf("pinned handshake failed: server=%v client=%v", serverErr, clientErr)
		}
	})

	t.Run("unknown certificate rejected at TLS layer", func(t *testing.T) {
		clientCfg, err := stranger.ClientTLS(importer.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		serverErr, _ := handshakePair(t, serverCfg, clientCfg)
		if serverErr == nil {
			t.Fatal("server accepted an unpinned client certificate")
		}
		// The server-side error carries the sentinel AND the presented
		// fingerprint, so the rejection is loggable with attribution.
		if !errors.Is(serverErr, ErrUnknownPeerCertificate) {
			t.Errorf("server error = %v, want ErrUnknownPeerCertificate", serverErr)
		}
		if !strings.Contains(serverErr.Error(), stranger.Fingerprint()) {
			t.Errorf("server error %q does not name the presented fingerprint", serverErr)
		}
	})

	t.Run("no client certificate rejected", func(t *testing.T) {
		// A client that sends no certificate at all is refused too.
		clientCfg := &tls.Config{
			MinVersion:            tls.VersionTLS13,
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: verifyPinnedPeer(importer.Fingerprint()),
		}
		serverErr, _ := handshakePair(t, serverCfg, clientCfg)
		if serverErr == nil {
			t.Fatal("server accepted a connection without a client certificate")
		}
	})
}
