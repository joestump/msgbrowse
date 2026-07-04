// doctor's device-sync checks (SPEC-0011 REQ "Status Surfacing": listener
// posture matches configuration, certificate validity/expiry, and the paired
// peer inventory). Scope for this story is posture + cert + peers; the
// pinned-mTLS reachability ping and staleness checks ride the status story —
// doctor stays network-silent except behind --check-llm.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"time"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/devices"
	"github.com/joestump/msgbrowse/internal/store"
)

// identityExpiryWarn is how far ahead of certificate expiry doctor starts
// warning. Rotation is re-pairing in v1 (ADR-0018), so the runway is long.
const identityExpiryWarn = 90 * 24 * time.Hour

// checkDeviceSync reports the device-sync posture. Disabled is the healthy
// default (pass, not warn — unlike archive roots, most installs never enable
// this). Enabled gets three checks: listener config sanity, identity
// presence/validity, and the paired-peer inventory.
func checkDeviceSync(ctx context.Context, r *report, cfg *config.Config, st *store.Store) {
	if !cfg.DeviceSync.Enabled {
		r.add(statusPass, "device sync disabled (no sync listener; loopback-only posture per ADR-0010)", "")
		return
	}

	checkDeviceSyncPorts(r, cfg)
	checkDeviceSyncIdentity(r, cfg)
	checkDeviceSyncPeers(ctx, r, st)
}

// checkDeviceSyncPorts re-validates the listener address here so the posture
// is visible in the report even though config.Validate gates it earlier:
// dedicated port, distinct from the web UI (SPEC-0011 "Sync Listener
// Posture").
func checkDeviceSyncPorts(r *report, cfg *config.Config) {
	_, syncPort, err := net.SplitHostPort(cfg.DeviceSync.ListenAddr)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("device_sync.listen_addr %q is not host:port: %v", cfg.DeviceSync.ListenAddr, err),
			"set device_sync.listen_addr to host:port with a port distinct from the web UI's")
		return
	}
	_, webPort, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		r.add(statusFail, fmt.Sprintf("listen_addr %q is not host:port: %v", cfg.ListenAddr, err), "")
		return
	}
	if syncPort == webPort {
		r.add(statusFail, fmt.Sprintf("device_sync.listen_addr %q shares the web UI port %s", cfg.DeviceSync.ListenAddr, webPort),
			"the sync listener needs its own port (SPEC-0011); change device_sync.listen_addr")
		return
	}
	r.add(statusPass, fmt.Sprintf("device sync enabled: listener on port %s (web UI on %s, mutual TLS only)", syncPort, webPort), "")
}

// checkDeviceSyncIdentity reports the node's TLS identity: present, loadable,
// and not near expiry (doctor warns well ahead — rotation is re-pairing).
func checkDeviceSyncIdentity(r *report, cfg *config.Config) {
	dir := devices.IdentityDir(cfg.DataDir)
	id, err := devices.LoadIdentityFromDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.add(statusWarn, "no device-sync identity yet",
			"generated automatically the first time the sync listener starts or `msgbrowse devices pair` runs")
		return
	case err != nil:
		r.add(statusFail, fmt.Sprintf("device-sync identity in %q is unreadable: %v", dir, err),
			"remove the devices/ directory to regenerate (already-paired peers must then re-pair)")
		return
	}

	expiry := id.Leaf.NotAfter
	switch {
	case time.Now().After(expiry):
		r.add(statusFail, fmt.Sprintf("device-sync certificate EXPIRED %s (fingerprint %s)",
			expiry.Local().Format("2006-01-02"), shortFingerprint(id.Fingerprint())),
			"peers reject expired certificates; remove the devices/ directory and re-pair every device")
	case time.Until(expiry) < identityExpiryWarn:
		r.add(statusWarn, fmt.Sprintf("device-sync certificate expires %s (fingerprint %s)",
			expiry.Local().Format("2006-01-02"), shortFingerprint(id.Fingerprint())),
			"rotation is re-pairing in v1: plan to remove the devices/ directory and re-pair before expiry")
	default:
		r.add(statusPass, fmt.Sprintf("device-sync identity present (fingerprint %s, expires %s)",
			shortFingerprint(id.Fingerprint()), expiry.Local().Format("2006-01-02")), "")
	}
}

// checkDeviceSyncPeers lists the paired peers from the registry.
func checkDeviceSyncPeers(ctx context.Context, r *report, st *store.Store) {
	if st == nil {
		r.add(statusWarn, "cannot list paired devices (no database yet)",
			"run `msgbrowse devices pair` to pair a device; the registry lives in the database")
		return
	}
	peers, err := st.ListPairedDevices(ctx)
	if err != nil {
		r.add(statusWarn, fmt.Sprintf("could not list paired devices: %v", err), "")
		return
	}
	if len(peers) == 0 {
		r.add(statusWarn, "device sync enabled but no devices paired yet",
			"run `msgbrowse devices pair` here and scan the QR (or paste the code) on the other device")
		return
	}
	names := make([]string, len(peers))
	for i, p := range peers {
		names[i] = fmt.Sprintf("%s (%s)", p.Name, shortFingerprint(p.Fingerprint))
	}
	r.add(statusPass, fmt.Sprintf("%s paired: %s", plural(len(peers), "device"), strings.Join(names, ", ")), "")
}
