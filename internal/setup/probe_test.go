package setup

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// errPermission is the injected "OS refused access" error the probes treat as a
// missing grant.
var errPermission = errors.New("operation not permitted")

// openMap returns an opener that succeeds for the listed paths (returning an
// empty reader, or the provided body) and fails with errPermission otherwise.
func openMap(bodies map[string]string) func(string) (io.ReadCloser, error) {
	return func(p string) (io.ReadCloser, error) {
		body, ok := bodies[p]
		if !ok {
			return nil, errPermission
		}
		return readCloser{strings.NewReader(body)}, nil
	}
}

func TestProbeFullDiskAccess(t *testing.T) {
	t.Run("no chat.db is not-applicable", func(t *testing.T) {
		// Empty HOME tree: chat.db absent → nothing to grant (the non-macOS case).
		d := Detector{Home: t.TempDir()}
		got := d.ProbeFullDiskAccess()
		if got.State != PermissionNotApplicable {
			t.Errorf("state = %v, want PermissionNotApplicable", got.State)
		}
	})

	t.Run("chat.db present but unreadable is needs-permission", func(t *testing.T) {
		home := t.TempDir()
		chatDB := filepath.Join(home, imessageRel)
		mkfile(t, chatDB)
		// Open fails for every path → FDA not granted.
		d := Detector{Home: home, Open: openMap(nil)}
		got := d.ProbeFullDiskAccess()
		if got.State != PermissionNeeded {
			t.Errorf("state = %v, want PermissionNeeded", got.State)
		}
		if got.Resource != chatDB {
			t.Errorf("resource = %q, want %q", got.Resource, chatDB)
		}
	})

	t.Run("chat.db present and readable is ok", func(t *testing.T) {
		home := t.TempDir()
		chatDB := filepath.Join(home, imessageRel)
		mkfile(t, chatDB)
		d := Detector{Home: home, Open: openMap(map[string]string{chatDB: ""})}
		got := d.ProbeFullDiskAccess()
		if got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK", got.State)
		}
	})

	t.Run("real filesystem open path (no injection)", func(t *testing.T) {
		// Exercise the default os.Open opener: a readable chat.db → OK.
		home := t.TempDir()
		mkfile(t, filepath.Join(home, imessageRel))
		if got := (Detector{Home: home}).ProbeFullDiskAccess(); got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK with real os.Open", got.State)
		}
	})
}

func TestProbeSignalKeychain(t *testing.T) {
	signalDir := func(home string) string { return filepath.Join(home, signalRel) }
	cfgPath := func(home string) string { return filepath.Join(signalDir(home), signalConfigRel) }

	t.Run("no config.json is not-applicable", func(t *testing.T) {
		home := t.TempDir()
		mkdir(t, signalDir(home)) // Signal dir but no config.json
		d := Detector{Home: home, Open: openMap(nil)}
		if got := d.ProbeSignalKeychain(); got.State != PermissionNotApplicable {
			t.Errorf("state = %v, want PermissionNotApplicable", got.State)
		}
	})

	t.Run("config without encryptedKey is ok", func(t *testing.T) {
		home := t.TempDir()
		d := Detector{Home: home, Open: openMap(map[string]string{
			cfgPath(home): `{"someOtherKey":"v"}`,
		})}
		if got := d.ProbeSignalKeychain(); got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK (no sealed key)", got.State)
		}
	})

	t.Run("unparseable config is ok (nothing to unseal)", func(t *testing.T) {
		home := t.TempDir()
		d := Detector{Home: home, Open: openMap(map[string]string{
			cfgPath(home): `not json`,
		})}
		if got := d.ProbeSignalKeychain(); got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK", got.State)
		}
	})

	t.Run("encryptedKey sealed, keychain inaccessible is needs-permission", func(t *testing.T) {
		home := t.TempDir()
		d := Detector{
			Home: home,
			Open: openMap(map[string]string{cfgPath(home): `{"encryptedKey":"deadbeef"}`}),
			// Default (nil) KeychainAccessible reports "cannot verify" → Needed.
		}
		got := d.ProbeSignalKeychain()
		if got.State != PermissionNeeded {
			t.Errorf("state = %v, want PermissionNeeded", got.State)
		}
	})

	t.Run("encryptedKey sealed, keychain accessible is ok", func(t *testing.T) {
		home := t.TempDir()
		var sawKey string
		d := Detector{
			Home: home,
			Open: openMap(map[string]string{cfgPath(home): `{"encryptedKey":"deadbeef"}`}),
			KeychainAccessible: func(k string) bool {
				sawKey = k
				return true // the injected "real macOS check" grants access
			},
		}
		if got := d.ProbeSignalKeychain(); got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK", got.State)
		}
		if sawKey != "deadbeef" {
			t.Errorf("keychain check saw key %q, want the encryptedKey value", sawKey)
		}
	})

	t.Run("empty home is not-applicable", func(t *testing.T) {
		if got := (Detector{Home: ""}).ProbeSignalKeychain(); got.State != PermissionNotApplicable {
			t.Errorf("state = %v, want PermissionNotApplicable", got.State)
		}
	})
}

func TestProbeWhatsAppContainer(t *testing.T) {
	t.Run("no container is not-applicable", func(t *testing.T) {
		if got := (Detector{Home: t.TempDir()}).ProbeWhatsAppContainer(); got.State != PermissionNotApplicable {
			t.Errorf("state = %v, want PermissionNotApplicable", got.State)
		}
	})

	t.Run("database present but unreadable is needs-permission", func(t *testing.T) {
		home := fakeHome(t, false, false, true)
		d := Detector{Home: home, Open: openMap(nil)} // open always fails
		got := d.ProbeWhatsAppContainer()
		if got.State != PermissionNeeded {
			t.Errorf("state = %v, want PermissionNeeded", got.State)
		}
		if filepath.Base(got.Resource) != whatsappDBName {
			t.Errorf("resource = %q, want the ChatStorage.sqlite", got.Resource)
		}
	})

	t.Run("database present and readable is ok", func(t *testing.T) {
		home := fakeHome(t, false, false, true)
		det := Detector{Home: home}.DetectWhatsApp()
		d := Detector{Home: home, Open: openMap(map[string]string{det.Path: ""})}
		if got := d.ProbeWhatsAppContainer(); got.State != PermissionOK {
			t.Errorf("state = %v, want PermissionOK", got.State)
		}
	})
}

func TestPermissionStateString(t *testing.T) {
	cases := map[PermissionState]string{
		PermissionOK:            "ok",
		PermissionNeeded:        "needs-permission",
		PermissionNotApplicable: "n/a",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", state, got, want)
		}
	}
}
