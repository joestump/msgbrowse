// The `msgbrowse devices` namespace: pair, list, unpair, and status for
// multi-device archive sync peers.
//
// Governing: ADR-0018 (QR pairing + pinned mutual TLS), SPEC-0011 REQ
// "Pairing Initiation" (a pairing window MUST be openable from a CLI
// command), REQ "Unpairing and Revocation" (unpair from the CLI, local and
// immediate), REQ "Status Surfacing" (a CLI status command). The `devices`
// noun is the canonical namespace per design.md — the `sync` verb belongs to
// ADR-0015's export→import pipeline.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/joestump/msgbrowse/internal/config"
	"github.com/joestump/msgbrowse/internal/devices"
	"github.com/joestump/msgbrowse/internal/devices/listener"
	"github.com/joestump/msgbrowse/internal/source"
	"github.com/joestump/msgbrowse/internal/store"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

func newDevicesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Pair and manage device-sync peers (mutual-TLS archive sync)",
		Long: "devices manages multi-device archive synchronization peers (ADR-0018).\n" +
			"Trust is established once, physically, by scanning a QR code or pasting a\n" +
			"manual pairing code; every connection afterwards is mutual TLS on the\n" +
			"certificate fingerprints pinned at pairing. Device sync is strictly opt-in:\n" +
			"set device_sync.enabled in the config before pairing.",
	}
	cmd.AddCommand(
		newDevicesPairCommand(),
		newDevicesListCommand(),
		newDevicesUnpairCommand(),
		newDevicesStatusCommand(),
	)
	return cmd
}

func newDevicesPairCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pair [payload|manual-code]",
		Short: "Open a pairing window (no argument) or pair with an importer (payload argument)",
		Long: "With no argument, pair runs on the IMPORTER: it opens a single-use pairing\n" +
			"window (token TTL ≤ 10 minutes), starts the device-sync listener, and prints\n" +
			"a QR code plus a copyable MSGB1. manual code. Scan or paste that on the\n" +
			"other device before the window expires.\n" +
			"\n" +
			"With an argument, pair runs on the REPLICA: pass the manual code (MSGB1.…)\n" +
			"or the raw payload JSON from the importer. The replica verifies the\n" +
			"importer's TLS certificate against the fingerprint in the payload BEFORE\n" +
			"the token is transmitted, then both sides pin each other's certificates.\n" +
			"\n" +
			"Requires device_sync.enabled: true on both nodes. The importer-side window\n" +
			"binds device_sync.listen_addr, so stop `msgbrowse serve` first if it is\n" +
			"running with device sync enabled on the same port.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if len(args) == 0 {
				return devicesPairImporter(ctx, cmd.OutOrStdout(), cfg)
			}
			return devicesPairReplica(ctx, cmd.OutOrStdout(), cfg, args[0])
		},
	}
}

func newDevicesListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List paired devices and their pinned fingerprints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			return devicesList(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}
}

func newDevicesUnpairCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unpair <fingerprint>",
		Short: "Unpair a device: remove its pinned certificate, immediately and locally",
		Long: "unpair removes the peer record and its pinned certificate fingerprint.\n" +
			"Revocation is local and immediate — the peer does not need to be reachable\n" +
			"or cooperative, and its certificate is refused from the next request on.\n" +
			"Already-synced archive files and the database are untouched (SPEC-0011).\n" +
			"\n" +
			"Pass the full 64-hex-character fingerprint from `devices list`, or a unique\n" +
			"prefix of at least 8 characters.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			return devicesUnpair(cmd.Context(), cmd.OutOrStdout(), cfg, args[0])
		},
	}
}

func newDevicesStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show device-sync posture: config, identity, and paired peers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig()
			if err != nil {
				return err
			}
			return devicesStatus(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}
}

// --- pairing: importer side ---------------------------------------------------

// devicesPairImporter opens a pairing window, starts the sync listener on
// device_sync.listen_addr, prints the QR + manual code, and serves until the
// window closes (paired, expired, rate-limited) or the context cancels.
func devicesPairImporter(ctx context.Context, out io.Writer, cfg *config.Config) error {
	if err := requireDeviceSync(cfg); err != nil {
		return err
	}
	// Resolve the address replicas will dial BEFORE binding, so a host we
	// cannot advertise fails fast.
	host, err := advertiseHost(cfg.DeviceSync.ListenAddr)
	if err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	name := deviceName(cfg)
	id, created, err := devices.LoadOrCreateIdentity(devices.IdentityDir(cfg.DataDir), name)
	if err != nil {
		return err
	}
	if created {
		slog.Info("generated device-sync identity", "fingerprint", id.Fingerprint())
	}

	// The close hook delivers the terminal window state; the buffered channel
	// means the hook (fired under the window lock) never blocks.
	closed := make(chan devices.WindowStatus, 1)
	window, err := devices.OpenWindow(0, devices.WithCloseHook(func(s devices.WindowStatus) {
		closed <- s
	}))
	if err != nil {
		return err
	}

	logger := newCharmLogger(cfg.LogLevel)
	imp := &devices.Importer{
		DeviceName: name,
		Sources:    importedSources(cfg),
		Store:      st,
		Logger:     logger,
	}
	imp.SetWindow(window)
	l := &listener.Listener{
		Identity: id,
		Importer: imp,
		Registry: storeRegistry{st},
		Addr:     cfg.DeviceSync.ListenAddr,
		Logger:   logger,
	}

	ln, err := l.Listen(ctx)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("bound address %q: %w", ln.Addr(), err)
	}
	payload, err := devices.NewPairingPayload(net.JoinHostPort(host, port), window.Token(), id.Fingerprint())
	if err != nil {
		_ = ln.Close()
		return err
	}
	printPairingPayload(out, payload, window.ExpiresAt())

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Serve(serveCtx, ln) }()

	// An untouched window never observes its own TTL (expiry latches on the
	// next Status/Consume), so poke it just past the deadline.
	expiry := time.NewTimer(time.Until(window.ExpiresAt()) + time.Second)
	defer expiry.Stop()

	for {
		select {
		case ws := <-closed:
			cancel()
			<-done
			return pairingOutcome(ctx, out, st, ws)
		case <-expiry.C:
			// Latches CloseExpired if the TTL elapsed; the hook then fires
			// into `closed` and the next iteration reports it.
			_ = window.Status()
		case <-ctx.Done():
			cancel()
			<-done
			fmt.Fprintln(out, "pairing cancelled; the window token is invalidated")
			return nil
		}
	}
}

// printPairingPayload renders the QR to the terminal (go-qrcode's terminal
// renderer) plus the manual MSGB1. code carrying the same fields — the
// manual code is the accessibility path and MUST always accompany the QR
// (SPEC-0011 "QR Code and Manual Code Fallback").
func printPairingPayload(out io.Writer, payload *devices.PairingPayload, expires time.Time) {
	fmt.Fprintf(out, "Pairing window open — expires %s (%s from now)\n",
		expires.Local().Format(time.Kitchen), time.Until(expires).Round(time.Second))
	fmt.Fprintf(out, "Listener endpoint: %s\n", payload.Endpoint)
	fmt.Fprintf(out, "Certificate fingerprint: %s\n\n", payload.Fingerprint)

	if qrBytes, err := payload.EncodeQR(); err == nil {
		if qr, qerr := qrcode.New(string(qrBytes), qrcode.Medium); qerr == nil {
			fmt.Fprintln(out, qr.ToSmallString(false))
		}
	}

	if code, err := payload.EncodeManualCode(); err == nil {
		fmt.Fprintf(out, "Manual pairing code (same fields as the QR):\n\n  %s\n\n", code)
		fmt.Fprintf(out, "On the other device run:\n\n  msgbrowse devices pair '%s'\n\n", code)
	}
}

// pairingOutcome turns the window's terminal status into the command result.
func pairingOutcome(ctx context.Context, out io.Writer, st *store.Store, ws devices.WindowStatus) error {
	switch ws.Reason {
	case devices.CloseConsumed:
		peers, err := st.ListPairedDevices(ctx)
		if err == nil && len(peers) > 0 {
			p := peers[len(peers)-1]
			fmt.Fprintf(out, "Paired with %s (%s) at %s\n", p.Name, p.Fingerprint, p.Address)
		} else {
			fmt.Fprintln(out, "Device paired.")
		}
		return nil
	case devices.CloseExpired:
		return fmt.Errorf("pairing window expired before any device paired — run `msgbrowse devices pair` again")
	case devices.CloseRateLimited:
		return fmt.Errorf("pairing window closed after %d consecutive failed attempts — verify the network and open a new window",
			devices.MaxPairingFailures)
	default:
		return fmt.Errorf("pairing window closed (%s) before any device paired", ws.Reason)
	}
}

// --- pairing: replica side ----------------------------------------------------

// devicesPairReplica completes pairing against a live importer listener:
// decode the payload, verify the importer's certificate against the payload
// fingerprint during the TLS handshake (abort before the token is sent on a
// mismatch), present the token, and pin the importer.
func devicesPairReplica(ctx context.Context, out io.Writer, cfg *config.Config, payloadArg string) error {
	if err := requireDeviceSync(cfg); err != nil {
		return err
	}
	payload, err := devices.DecodePayload([]byte(payloadArg))
	if err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	name := deviceName(cfg)
	id, created, err := devices.LoadOrCreateIdentity(devices.IdentityDir(cfg.DataDir), name)
	if err != nil {
		return err
	}
	if created {
		slog.Info("generated device-sync identity", "fingerprint", id.Fingerprint())
	}

	client, err := id.NewPeerClient(payload.Fingerprint, 30*time.Second)
	if err != nil {
		return err
	}

	peer, err := devices.Pair(ctx, client, payload, devices.PairRequest{
		Token:        payload.Token,
		DeviceName:   name,
		ListenerAddr: replicaAdvertiseAddr(cfg),
	}, st, time.Now())
	if err != nil {
		if errors.Is(err, devices.ErrFingerprintMismatch) {
			return fmt.Errorf("the device at %s presented a certificate that does NOT match the pairing code "+
				"(possible wrong device or man-in-the-middle); nothing was sent: %w", payload.Endpoint, err)
		}
		return err
	}

	fmt.Fprintf(out, "Paired with %s (%s) at %s\n", peer.Name, peer.Fingerprint, peer.Address)
	fmt.Fprintf(out, "Sources served: %s\n", strings.Join(sortedRoleSources(peer.Roles), ", "))
	return nil
}

// replicaAdvertiseAddr is the replica listener address sent to the importer
// (advisory: where post-ingest notifications will be delivered once the
// steady-state story lands). Best effort — an underivable host falls back to
// the configured spelling.
func replicaAdvertiseAddr(cfg *config.Config) string {
	host, err := advertiseHost(cfg.DeviceSync.ListenAddr)
	if err != nil {
		return cfg.DeviceSync.ListenAddr
	}
	_, port, err := net.SplitHostPort(cfg.DeviceSync.ListenAddr)
	if err != nil {
		return cfg.DeviceSync.ListenAddr
	}
	return net.JoinHostPort(host, port)
}

// --- list / unpair / status ----------------------------------------------------

func devicesList(ctx context.Context, out io.Writer, cfg *config.Config) error {
	if !fileExists(dbPath(cfg)) {
		fmt.Fprintln(out, "no devices paired")
		return nil
	}
	st, err := store.OpenReadOnly(dbPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()

	peers, err := st.ListPairedDevices(ctx)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Fprintln(out, "no devices paired")
		return nil
	}
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tFINGERPRINT\tADDRESS\tROLES\tPAIRED")
	for _, p := range peers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			p.Name, p.Fingerprint, p.Address, rolesString(p.Roles), p.PairedAt.Local().Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

func devicesUnpair(ctx context.Context, out io.Writer, cfg *config.Config, fpArg string) error {
	if !fileExists(dbPath(cfg)) {
		return fmt.Errorf("no devices paired")
	}
	st, err := store.Open(dbPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()

	peers, err := st.ListPairedDevices(ctx)
	if err != nil {
		return err
	}
	peer, err := matchPeerFingerprint(peers, fpArg)
	if err != nil {
		return err
	}
	if err := st.DeletePairedDevice(ctx, peer.ID); err != nil {
		return err
	}
	fmt.Fprintf(out, "Unpaired %s (%s); its certificate is no longer accepted.\n", peer.Name, peer.Fingerprint)
	fmt.Fprintln(out, "Already-synced archive files and the database are untouched.")
	return nil
}

// matchPeerFingerprint resolves a user-supplied fingerprint — full 64-hex or
// a unique prefix of at least minFingerprintPrefix characters (colons and
// case tolerated) — to exactly one peer.
const minFingerprintPrefix = 8

func matchPeerFingerprint(peers []devices.Peer, fpArg string) (*devices.Peer, error) {
	needle := strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(strings.TrimSpace(fpArg)))
	if len(needle) < minFingerprintPrefix {
		return nil, fmt.Errorf("fingerprint %q is too short: pass at least %d characters (see `msgbrowse devices list`)",
			fpArg, minFingerprintPrefix)
	}
	var matches []devices.Peer
	for _, p := range peers {
		if strings.HasPrefix(p.Fingerprint, needle) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no paired device matches fingerprint %q (see `msgbrowse devices list`)", fpArg)
	case 1:
		return &matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return nil, fmt.Errorf("fingerprint prefix %q is ambiguous (matches %s); pass more characters",
			fpArg, strings.Join(names, ", "))
	}
}

func devicesStatus(ctx context.Context, out io.Writer, cfg *config.Config) error {
	if !cfg.DeviceSync.Enabled {
		fmt.Fprintln(out, "device sync: disabled (no sync listener; loopback-only posture per ADR-0010)")
	} else {
		fmt.Fprintln(out, "device sync: enabled")
		fmt.Fprintf(out, "  listener:      %s (mutual TLS 1.3, pinned fingerprints)\n", cfg.DeviceSync.ListenAddr)
		fmt.Fprintf(out, "  device name:   %s\n", deviceName(cfg))
		fmt.Fprintf(out, "  poll interval: %s\n", cfg.DeviceSync.PollInterval)
	}

	id, err := devices.LoadIdentityFromDir(devices.IdentityDir(cfg.DataDir))
	switch {
	case err == nil:
		fmt.Fprintf(out, "  identity:      %s (expires %s)\n", id.Fingerprint(), id.Leaf.NotAfter.Local().Format("2006-01-02"))
	case os.IsNotExist(err) || errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(out, "  identity:      none yet (generated on first pair or listener start)")
	default:
		fmt.Fprintf(out, "  identity:      ERROR: %v\n", err)
	}

	if !fileExists(dbPath(cfg)) {
		fmt.Fprintln(out, "  peers:         none")
		return nil
	}
	st, err := store.OpenReadOnly(dbPath(cfg))
	if err != nil {
		return err
	}
	defer st.Close()
	peers, err := st.ListPairedDevices(ctx)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Fprintln(out, "  peers:         none")
		return nil
	}
	fmt.Fprintf(out, "  peers:         %d\n", len(peers))
	for _, p := range peers {
		fmt.Fprintf(out, "    %s (%s) at %s — %s, paired %s\n",
			p.Name, shortFingerprint(p.Fingerprint), p.Address, rolesString(p.Roles),
			p.PairedAt.Local().Format("2006-01-02"))
		// For sources this peer imports (we replicate), show the last adopted
		// manifest generation; 0 means no sync round yet. Transfer progress
		// surfacing rides the transfer story (#106).
		for _, src := range sortedRoleSources(p.Roles) {
			if p.Roles[src] != devices.RoleImporter {
				continue
			}
			gen, gerr := st.SyncGeneration(ctx, p.ID, src)
			if gerr != nil {
				continue
			}
			fmt.Fprintf(out, "      %s: last adopted manifest generation %d\n", src, gen)
		}
	}
	return nil
}

// --- shared helpers -------------------------------------------------------------

// requireDeviceSync gates pairing commands on the opt-in flag.
func requireDeviceSync(cfg *config.Config) error {
	if !cfg.DeviceSync.Enabled {
		return fmt.Errorf("device sync is disabled: set device_sync.enabled: true " +
			"(or MSGBROWSE_DEVICE_SYNC_ENABLED=true) — it is strictly opt-in (ADR-0018)")
	}
	return nil
}

// deviceName resolves this node's device name: the configured
// device_sync.device_name, else the hostname.
func deviceName(cfg *config.Config) string {
	if cfg.DeviceSync.DeviceName != "" {
		return cfg.DeviceSync.DeviceName
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "msgbrowse"
}

// importedSources lists the sources THIS node imports: the ones whose
// archive roots are configured here (ADR-0018: the node that runs a source's
// exporters is its importer).
func importedSources(cfg *config.Config) []string {
	var out []string
	if cfg.ArchiveRoot != "" {
		out = append(out, source.Signal)
	}
	if cfg.IMessageArchiveRoot != "" {
		out = append(out, source.IMessage)
	}
	if cfg.WhatsAppArchiveRoot != "" {
		out = append(out, source.WhatsApp)
	}
	return out
}

// advertiseHost resolves the host peers should dial for listenAddr. An
// explicit host is used as-is; a wildcard/empty host (":8788", "0.0.0.0:…",
// "[::]:…") is replaced with this machine's LAN address, since "every
// interface" is not a dialable endpoint for the QR payload.
func advertiseHost(listenAddr string) (string, error) {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("invalid device_sync.listen_addr %q: %w", listenAddr, err)
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		return host, nil
	}
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("enumerate interfaces for the pairing endpoint: %w", err)
	}
	if lan := lanHostFrom(addrs); lan != "" {
		return lan, nil
	}
	return "", fmt.Errorf("could not determine a LAN address to advertise for %q; "+
		"set an explicit host in device_sync.listen_addr (e.g. \"192.168.1.10:8788\")", listenAddr)
}

// interfaceAddrs is swappable in tests.
var interfaceAddrs = func() ([]net.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		out = append(out, addrs...)
	}
	return out, nil
}

// lanHostFrom picks the address to advertise from interface addresses:
// the first global-unicast IPv4, else the first global-unicast IPv6, else "".
// Pure so it is testable without touching real interfaces.
func lanHostFrom(addrs []net.Addr) string {
	var v6 string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			continue
		}
		if ip.To4() != nil {
			return ip.String()
		}
		if v6 == "" {
			v6 = ip.String()
		}
	}
	return v6
}

func rolesString(roles map[string]devices.Role) string {
	parts := make([]string, 0, len(roles))
	for _, src := range sortedRoleSources(roles) {
		parts = append(parts, fmt.Sprintf("%s:%s", src, roles[src]))
	}
	return strings.Join(parts, ",")
}

func sortedRoleSources(roles map[string]devices.Role) []string {
	out := make([]string, 0, len(roles))
	for src := range roles {
		out = append(out, src)
	}
	sort.Strings(out)
	return out
}

func shortFingerprint(fp string) string {
	if len(fp) <= 16 {
		return fp
	}
	return fp[:16] + "…"
}

// storeRegistry adapts *store.Store to listener.Registry: the pin question
// is a paired_devices lookup, so revocation (row deletion) is visible to the
// listener on the very next handshake/request.
type storeRegistry struct{ st *store.Store }

func (s storeRegistry) IsPinned(ctx context.Context, fingerprint string) (bool, error) {
	_, err := s.st.GetPairedDeviceByFingerprint(ctx, fingerprint)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, devices.ErrUnknownPeerCertificate):
		return false, nil
	default:
		return false, err
	}
}

func (s storeRegistry) PairedCount(ctx context.Context) (int, error) {
	peers, err := s.st.ListPairedDevices(ctx)
	if err != nil {
		return 0, err
	}
	return len(peers), nil
}
