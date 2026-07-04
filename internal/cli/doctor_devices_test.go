// doctor's device-sync rows (SPEC-0011 REQ "Status Surfacing"): posture
// (disabled = healthy default / enabled = dedicated port), identity
// presence + expiry runway, and the paired-peer inventory.
package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/devices"
)

// doctorDeviceCfg is a minimal config whose data dir exists (so checkDataDir
// proceeds far enough to hand later checks a store when a DB exists).
func doctorDeviceCfg(t *testing.T, enabled bool) *config.Config {
	t.Helper()
	return &config.Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:8787",
		LogLevel:   "error",
		DeviceSync: config.DeviceSyncConfig{
			Enabled:      enabled,
			ListenAddr:   ":8788",
			PollInterval: 15 * time.Minute,
		},
	}
}

func doctorOutput(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var out bytes.Buffer
	runDoctor(context.Background(), &out, cfg, false)
	return out.String()
}

func TestDoctorDeviceSyncDisabled(t *testing.T) {
	out := doctorOutput(t, doctorDeviceCfg(t, false))
	if !strings.Contains(out, "device sync disabled") {
		t.Errorf("doctor output missing the disabled-posture row:\n%s", out)
	}
	if strings.Contains(out, "device sync enabled") {
		t.Errorf("disabled config must not report an enabled listener:\n%s", out)
	}
}

func TestDoctorDeviceSyncEnabledFreshInstall(t *testing.T) {
	out := doctorOutput(t, doctorDeviceCfg(t, true))
	for _, want := range []string{
		"device sync enabled: listener on port 8788 (web UI on 8787",
		"no device-sync identity yet",
		"cannot list paired devices (no database yet)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorDeviceSyncPortClash(t *testing.T) {
	cfg := doctorDeviceCfg(t, true)
	cfg.DeviceSync.ListenAddr = ":8787" // same port as the web UI, different spelling
	out := doctorOutput(t, cfg)
	if !strings.Contains(out, "shares the web UI port 8787") {
		t.Errorf("doctor output missing the port-clash failure:\n%s", out)
	}
}

func TestDoctorDeviceSyncHealthy(t *testing.T) {
	cfg := doctorDeviceCfg(t, true)
	id, _, err := devices.LoadOrCreateIdentity(devices.IdentityDir(cfg.DataDir), "mac-importer")
	if err != nil {
		t.Fatal(err)
	}
	seedPeer(t, cfg, devices.Peer{
		Name: "kitchen-server", Fingerprint: strings.Repeat("a", 64), Address: "192.168.1.20:8788",
		Roles: map[string]devices.Role{"signal": devices.RoleReplica}, PairedAt: time.Now(),
	})

	out := doctorOutput(t, cfg)
	for _, want := range []string{
		"device-sync identity present (fingerprint " + shortFingerprint(id.Fingerprint()),
		"1 device paired: kitchen-server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// writeIdentityWithLifetime persists an identity with a custom lifetime so
// expiry verdicts are testable (LoadOrCreateIdentity always mints the
// default 10-year cert).
func writeIdentityWithLifetime(t *testing.T, dataDir string, lifetime time.Duration) {
	t.Helper()
	id, err := devices.NewIdentity("aging-node", lifetime)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := id.EncodePEM()
	if err != nil {
		t.Fatal(err)
	}
	dir := devices.IdentityDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, devices.IdentityCertFile), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, devices.IdentityKeyFile), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorDeviceSyncCertExpiry(t *testing.T) {
	t.Run("near expiry warns", func(t *testing.T) {
		cfg := doctorDeviceCfg(t, true)
		writeIdentityWithLifetime(t, cfg.DataDir, 30*24*time.Hour) // inside the 90-day runway
		out := doctorOutput(t, cfg)
		if !strings.Contains(out, "device-sync certificate expires") {
			t.Errorf("doctor output missing the near-expiry warning:\n%s", out)
		}
	})
	t.Run("expired fails", func(t *testing.T) {
		cfg := doctorDeviceCfg(t, true)
		writeIdentityWithLifetime(t, cfg.DataDir, time.Nanosecond)
		out := doctorOutput(t, cfg)
		if !strings.Contains(out, "device-sync certificate EXPIRED") {
			t.Errorf("doctor output missing the expired failure:\n%s", out)
		}
	})
}
