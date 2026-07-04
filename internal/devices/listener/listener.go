// Package listener is msgbrowse's device-sync LAN listener: the FIRST socket
// in the app's history that accepts connections beyond loopback (ADR-0018,
// which amends ADR-0010's loopback-only posture). It mounts the pairing and
// trust primitives from internal/devices on a real TLS socket and serves
// ONLY the devices API — never the web UI, never HTML.
//
// Posture (SPEC-0011 "Sync Listener Posture" + Security Requirements):
//
//   - Off by default: this package is only constructed when
//     device_sync.enabled is true; the caller decides, this package binds.
//   - Own port: the address is the device_sync.listen_addr, validated by
//     internal/config to use a port distinct from the web UI's.
//   - TLS 1.3 minimum, client certificates required on every connection.
//   - POST /v1/pair is the single pre-trust endpoint: reachable by unpinned
//     certificates only while a pairing window is open, gated by the
//     single-use token (internal/devices.Window: constant-time compare,
//     TTL <= 10 min, five-consecutive-failure closure).
//   - Everything else requires an exact pinned-fingerprint match: unknown
//     certificates are rejected at the TLS layer whenever no pairing window
//     is open, and rejected per-request at the HTTP layer while one is
//     (pairing must share the port, so the handshake cannot be the only
//     gate during a window). Both rejections log the presented fingerprint.
//   - Responses carry accurate Content-Type + X-Content-Type-Options:
//     nosniff, and no redirect is ever emitted (the router matches exact
//     paths — no http.ServeMux path canonicalization, which can 301).
//
// Lifecycle (SPEC-0011 "Concurrency Safety"): Run is a context-managed
// worker — cancel the context and the listener stops accepting, drains
// in-flight requests, and invalidates any open pairing window on the way
// down (disabling device sync must kill the window's token).
package listener

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/charmbracelet/log"
	"github.com/joestump/msgbrowse/internal/devices"
)

// PingPath is the mTLS reachability probe (SPEC-0011 Authentication table:
// GET /v1/ping, pinned peers only). doctor and `devices status` dial it.
const PingPath = "/v1/ping"

// shutdownTimeout bounds the graceful drain after the context cancels.
const shutdownTimeout = 5 * time.Second

// registryTimeout bounds the pin lookups performed inside TLS handshakes,
// where no request context exists yet.
const registryTimeout = 5 * time.Second

// Registry is the slice of the paired_devices registry the listener needs:
// the TLS layer's "is this fingerprint pinned?" and the bind log's peer
// count. Implemented by *store.Store (via the CLI's adapter) and by fakes in
// tests.
type Registry interface {
	// IsPinned reports whether fingerprint is pinned in the peer registry.
	IsPinned(ctx context.Context, fingerprint string) (bool, error)
	// PairedCount returns the number of paired peers (bind-time log line,
	// per SPEC-0011 "Listener startup MUST log ... the number of paired
	// peers").
	PairedCount(ctx context.Context) (int, error)
}

// PingResponse is the JSON body GET /v1/ping returns to a pinned peer.
type PingResponse struct {
	// Status is always "ok" — reaching the handler at all proves mutual TLS
	// with a pinned certificate succeeded.
	Status string `json:"status"`
	// DeviceName is this node's device name, so probes can confirm they
	// reached the peer they meant.
	DeviceName string `json:"device_name"`
}

// Listener serves the devices API over mutual TLS. Construct one with every
// field set, then call Run (or Listen + Serve when the caller needs the
// bound address first, e.g. `devices pair` embedding the real port in the
// QR payload).
type Listener struct {
	// Identity is this node's long-lived TLS identity (the certificate whose
	// fingerprint peers pin).
	Identity *devices.Identity
	// Importer owns the pairing window and the /v1/pair exchange.
	Importer *devices.Importer
	// Registry answers pin lookups against paired_devices.
	Registry Registry
	// Addr is the configured bind address (host:port), normally
	// device_sync.listen_addr.
	Addr string
	// Logger receives structured listener events; nil uses log.Default().
	Logger *log.Logger
}

func (l *Listener) logger() *log.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return log.Default()
}

// tlsConfig is the listener's single TLS posture: TLS 1.3 minimum, a client
// certificate always required, and verification split by trust state —
// pinned fingerprints always pass; unpinned ones complete the handshake ONLY
// while a pairing window is open (they can then reach nothing but the
// token-gated /v1/pair — Handler enforces that); with no window open they
// are rejected at the TLS layer and logged with the presented fingerprint
// (SPEC-0011 "Unknown certificate rejected after pairing").
func (l *Listener) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{l.Identity.TLSCertificate},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("%w: peer presented no certificate", devices.ErrUnknownPeerCertificate)
			}
			fp := devices.Fingerprint(rawCerts[0])
			ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
			defer cancel()
			pinned, err := l.Registry.IsPinned(ctx, fp)
			if err != nil {
				l.logger().Error("pin lookup failed during TLS handshake", "err", err, "peer_fingerprint", fp)
				return fmt.Errorf("devices listener: pin lookup: %w", err)
			}
			if pinned {
				return nil
			}
			if w := l.Importer.CurrentWindow(); w != nil && w.Status().Open {
				// Pre-trust connection during an open pairing window: allowed
				// through the handshake so /v1/pair is reachable; the HTTP
				// layer refuses it everywhere else.
				return nil
			}
			l.logger().Warn("rejected unknown peer certificate at the TLS layer",
				"peer_fingerprint", fp)
			return fmt.Errorf("%w: presented fingerprint %s", devices.ErrUnknownPeerCertificate, fp)
		},
	}
}

// Handler routes the devices API. The router matches exact paths in a switch
// — deliberately NOT http.ServeMux, whose path canonicalization emits 301s,
// and the sync protocol never emits redirects (SPEC-0011 "Redirect
// Validation").
func (l *Listener) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		switch r.URL.Path {
		case devices.PairPath:
			// The single pre-trust endpoint: token-gated by the Importer
			// (window open, single-use, constant-time, rate-limited).
			l.Importer.PairHandler().ServeHTTP(w, r)
		case PingPath:
			l.requirePinned(http.HandlerFunc(l.handlePing)).ServeHTTP(w, r)
		default:
			// Unknown paths still require a pinned peer: a pre-trust client
			// probing during a pairing window learns nothing about the route
			// table, and gets the same 403 as any other unpinned request.
			l.requirePinned(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusNotFound, errorBody{Error: "not_found"})
			})).ServeHTTP(w, r)
		}
	})
}

// requirePinned enforces the pinned-peer mTLS gate per request on every
// non-pairing endpoint. The TLS layer already rejects unpinned certificates
// whenever no pairing window is open; this layer covers the two remaining
// windows of exposure: pre-trust connections that completed a handshake
// because a pairing window was open, and connections whose peer was UNPAIRED
// after the handshake (revocation must take effect immediately, not at the
// next handshake — SPEC-0011 "Unpairing and Revocation").
func (l *Listener) requirePinned(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			// Unreachable when mounted behind tlsConfig (which requires a
			// client cert); kept so a mis-mount fails closed.
			writeJSON(w, http.StatusForbidden, errorBody{Error: "client_certificate_required"})
			return
		}
		fp := devices.Fingerprint(r.TLS.PeerCertificates[0].Raw)
		pinned, err := l.Registry.IsPinned(r.Context(), fp)
		if err != nil {
			l.logger().Error("pin lookup failed", "err", err, "peer_fingerprint", fp, "path", r.URL.Path)
			writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal"})
			return
		}
		if !pinned {
			l.logger().Warn("rejected unpinned certificate on mTLS endpoint",
				"peer_fingerprint", fp, "path", r.URL.Path)
			writeJSON(w, http.StatusForbidden, errorBody{Error: "unknown_peer"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handlePing answers the mTLS reachability probe.
func (l *Listener) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: "method_not_allowed"})
		return
	}
	writeJSON(w, http.StatusOK, PingResponse{Status: "ok", DeviceName: l.Importer.DeviceName})
}

// Listen binds Addr and logs the bind LOUDLY: this is the first socket
// beyond loopback in an app whose security posture was built on never having
// one, so the line says exactly that and names the port, the mTLS posture,
// and the paired-peer count (SPEC-0011 "Sync Listener Posture"; ADR-0018's
// amendment of ADR-0010). Passing a ":0" port yields an ephemeral port; the
// caller discovers it from the returned listener's Addr.
func (l *Listener) Listen(ctx context.Context) (net.Listener, error) {
	base, err := net.Listen("tcp", l.Addr)
	if err != nil {
		return nil, fmt.Errorf("devices listener: bind %s: %w", l.Addr, err)
	}
	bound := base.Addr().String()
	_, port, _ := net.SplitHostPort(bound)

	peers, err := l.Registry.PairedCount(ctx)
	if err != nil {
		// The count is a log detail; do not refuse to serve over it.
		l.logger().Warn("could not count paired peers for the bind log", "err", err)
		peers = -1
	}
	l.logger().Warn("device-sync listener bound: msgbrowse's FIRST listener beyond loopback (ADR-0018 amends ADR-0010's loopback-only posture)",
		"addr", bound,
		"port", port,
		"posture", "mutual TLS 1.3 with pinned certificate fingerprints; /v1/pair token-gated, only while a pairing window is open",
		"paired_peers", peers,
	)
	return tls.NewListener(base, l.tlsConfig()), nil
}

// Serve serves the devices API on ln until ctx cancels, then shuts down
// gracefully: stop accepting, drain in-flight requests (bounded by
// shutdownTimeout), and invalidate any open pairing window — a stopping
// listener must not leave a live token behind (SPEC-0011 "Disabling device
// sync MUST stop the listener and invalidate any open pairing window").
// Serve closes ln on return.
func (l *Listener) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           l.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		if w := l.Importer.CurrentWindow(); w != nil {
			w.Disable()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("devices listener: shutdown: %w", err)
		}
		l.logger().Info("device-sync listener stopped")
		return nil
	case err := <-errCh:
		if w := l.Importer.CurrentWindow(); w != nil {
			w.Disable()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("devices listener: serve: %w", err)
	}
}

// Run is Listen followed by Serve: the context-managed worker `serve` runs
// alongside the web UI when device sync is enabled.
func (l *Listener) Run(ctx context.Context) error {
	ln, err := l.Listen(ctx)
	if err != nil {
		return err
	}
	return l.Serve(ctx, ln)
}

// errorBody is the JSON error envelope for listener-level rejections,
// mirroring the pairing handler's {"error": code} shape.
type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
