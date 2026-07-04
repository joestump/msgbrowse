// Governing: SPEC-0011 REQ "Pairing Acceptance and Mutual Certificate
// Pinning" — the long-lived keypair is generated once at enablement and
// reloaded verbatim afterwards, so the fingerprint peers pinned never
// changes underneath them.
package devices

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentityRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "devices")

	id1, created, err := LoadOrCreateIdentity(dir, "mac-importer")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first call should create the identity")
	}

	// A second call loads the SAME identity — the pinnable fingerprint is
	// stable across restarts.
	id2, created, err := LoadOrCreateIdentity(dir, "mac-importer")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("second call should load, not create")
	}
	if id1.Fingerprint() != id2.Fingerprint() {
		t.Errorf("fingerprint changed across reload: %s != %s", id1.Fingerprint(), id2.Fingerprint())
	}

	// The private key is 0600 (never group/world readable).
	info, err := os.Stat(filepath.Join(dir, IdentityKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity key mode = %o, want 600", perm)
	}
}

func TestLoadIdentityFromDirMissing(t *testing.T) {
	_, err := LoadIdentityFromDir(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing identity error = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadIdentityFromDirCorrupt(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{IdentityCertFile, IdentityKeyFile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("not pem"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadIdentityFromDir(dir); err == nil {
		t.Fatal("corrupt identity loaded without error")
	}
}
