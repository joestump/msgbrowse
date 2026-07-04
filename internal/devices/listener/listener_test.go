// Governing: SPEC-0011 REQ "Sync Listener Posture", REQ "Pairing Acceptance
// and Mutual Certificate Pinning", Security Requirements (endpoint table,
// rate limiting, redirect validation) — the pairing flow end-to-end over a
// REAL loopback TLS socket, rate-limit lockout, replay rejection, TLS-layer
// rejection of unpinned certificates, immediate revocation, and the
// context-managed lifecycle. Run with -race per REQ "Concurrency Safety".
package listener

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/joestump/msgbrowse/internal/devices"
)

// fakeRegistry is an in-memory pin registry.
type fakeRegistry struct {
	mu   sync.Mutex
	pins map[string]bool
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{pins: map[string]bool{}} }

func (f *fakeRegistry) pin(fp string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins[fp] = true
}

func (f *fakeRegistry) unpin(fp string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pins, fp)
}

func (f *fakeRegistry) IsPinned(_ context.Context, fp string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pins[fp], nil
}

func (f *fakeRegistry) PairedCount(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pins), nil
}

// pinningStore is a devices.PeerStore that records peers AND pins their
// fingerprints in the registry — exactly what the production adapter does,
// where the store and the registry are the same paired_devices table.
type pinningStore struct {
	mu    sync.Mutex
	reg   *fakeRegistry
	peers []devices.Peer
}

func (s *pinningStore) UpsertPairedDevice(_ context.Context, p devices.Peer) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers = append(s.peers, p)
	s.reg.pin(p.Fingerprint)
	return int64(len(s.peers)), nil
}

func (s *pinningStore) all() []devices.Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]devices.Peer(nil), s.peers...)
}

func mustIdentity(t *testing.T, name string) *devices.Identity {
	t.Helper()
	id, err := devices.NewIdentity(name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// harness is one running listener on a real loopback TLS socket.
type harness struct {
	l        *Listener
	id       *devices.Identity
	reg      *fakeRegistry
	store    *pinningStore
	addr     string // bound host:port
	logs     *bytes.Buffer
	shutdown func()
	done     chan error
}

// startListener binds 127.0.0.1:0 and serves until the test ends (or the
// returned shutdown func runs). window may be nil (no window ever opened).
func startListener(t *testing.T, window *devices.Window) *harness {
	t.Helper()
	h := &harness{
		id:   mustIdentity(t, "mac-importer"),
		reg:  newFakeRegistry(),
		logs: &bytes.Buffer{},
	}
	h.store = &pinningStore{reg: h.reg}
	imp := &devices.Importer{
		DeviceName: "mac-importer",
		Sources:    []string{"signal", "imessage"},
		Store:      h.store,
		Logger:     log.New(io.Discard),
	}
	if window != nil {
		imp.SetWindow(window)
	}
	h.l = &Listener{
		Identity: h.id,
		Importer: imp,
		Registry: h.reg,
		Addr:     "127.0.0.1:0",
		Logger:   log.New(h.logs),
	}

	ctx, cancel := context.WithCancel(context.Background())
	ln, err := h.l.Listen(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	h.done = make(chan error, 1)
	go func() { h.done <- h.l.Serve(ctx, ln) }()
	var once sync.Once
	h.shutdown = func() {
		once.Do(func() {
			cancel()
			select {
			case <-h.done:
			case <-time.After(10 * time.Second):
				t.Error("listener did not shut down within 10s")
			}
		})
	}
	t.Cleanup(h.shutdown)
	return h
}

func openWindow(t *testing.T) *devices.Window {
	t.Helper()
	w, err := devices.OpenWindow(0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (h *harness) payload(t *testing.T, w *devices.Window) *devices.PairingPayload {
	t.Helper()
	p, err := devices.NewPairingPayload(h.addr, w.Token(), h.id.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func client(t *testing.T, id *devices.Identity, pinnedFP string) *http.Client {
	t.Helper()
	c, err := id.NewPeerClient(pinnedFP, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPairEndToEndOverTLS drives the full pairing handshake against a live
// TLS socket — fingerprint-verified handshake, token consume, mutual pinning
// — then proves the freshly pinned replica can reach the mTLS ping endpoint
// (design.md "Pairing handshake" sequence, over a real wire this time).
func TestPairEndToEndOverTLS(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)
	replica := mustIdentity(t, "kitchen-server")
	payload := h.payload(t, w)
	replicaStore := &pinningStore{reg: newFakeRegistry()}

	peer, err := devices.Pair(context.Background(), client(t, replica, payload.Fingerprint), payload,
		devices.PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		replicaStore, time.Now())
	if err != nil {
		t.Fatalf("Pair over TLS socket: %v", err)
	}
	if peer.Fingerprint != h.id.Fingerprint() {
		t.Errorf("replica pinned %s, want importer fingerprint", peer.Fingerprint)
	}

	// Importer side pinned the replica.
	peers := h.store.all()
	if len(peers) != 1 || peers[0].Fingerprint != replica.Fingerprint() {
		t.Fatalf("importer store = %+v, want the replica pinned", peers)
	}
	if st := w.Status(); st.Open || st.Reason != devices.CloseConsumed {
		t.Errorf("window status = %+v, want closed/consumed", st)
	}

	// The pinned replica can now reach the mTLS ping endpoint.
	resp, err := client(t, replica, payload.Fingerprint).Get("https://" + h.addr + PingPath)
	if err != nil {
		t.Fatalf("pinned ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("ping Content-Type = %q, want application/json", ct)
	}
	if ns := resp.Header.Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("ping X-Content-Type-Options = %q, want nosniff", ns)
	}
	var pr PingResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatal(err)
	}
	if pr.Status != "ok" || pr.DeviceName != "mac-importer" {
		t.Errorf("ping response = %+v", pr)
	}
}

// TestBindLogIsLoud: the bind line must announce the first-beyond-loopback
// posture, the port, the mTLS posture, and the paired-peer count (SPEC-0011
// "Sync Listener Posture"; ADR-0018 amending ADR-0010).
func TestBindLogIsLoud(t *testing.T) {
	h := startListener(t, nil)
	logged := h.logs.String()
	_, port, _ := net.SplitHostPort(h.addr)
	for _, want := range []string{
		"FIRST listener beyond loopback",
		"ADR-0018",
		"ADR-0010",
		"port=" + port,
		"mutual TLS 1.3",
		"paired_peers=0",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("bind log missing %q; got:\n%s", want, logged)
		}
	}
}

// TestUnknownCertRejectedAtTLSLayer: with no pairing window open, an
// unpinned certificate must fail the TLS handshake itself — no HTTP response
// exists (SPEC-0011 "Unknown certificate rejected after pairing") — and the
// rejection is logged with the presented fingerprint.
func TestUnknownCertRejectedAtTLSLayer(t *testing.T) {
	h := startListener(t, nil) // no window, ever
	stranger := mustIdentity(t, "stranger")

	_, err := client(t, stranger, h.id.Fingerprint()).Get("https://" + h.addr + PingPath)
	if err == nil {
		t.Fatal("unpinned client completed a request; want TLS handshake failure")
	}
	// The handshake failure must have been logged with the fingerprint.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(h.logs.String(), stranger.Fingerprint()) {
		if time.Now().After(deadline) {
			t.Fatalf("rejection log missing presented fingerprint; got:\n%s", h.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUnpinnedForbiddenOnMTLSEndpointsDuringWindow: while a pairing window
// is open an unpinned certificate can complete the handshake (it must, to
// reach /v1/pair) but every other endpoint refuses it per-request — /v1/pair
// stays the ONLY pre-trust endpoint (SPEC-0011 Authentication table).
func TestUnpinnedForbiddenOnMTLSEndpointsDuringWindow(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)
	stranger := mustIdentity(t, "stranger")
	c := client(t, stranger, h.id.Fingerprint())

	for _, path := range []string{PingPath, "/v1/manifest", "/"} {
		resp, err := c.Get("https://" + h.addr + path)
		if err != nil {
			t.Fatalf("GET %s during window: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s = %d (%s), want 403 for unpinned cert", path, resp.StatusCode, body)
		}
	}
}

// TestRateLimitLockout: five consecutive bad tokens close the window
// (SPEC-0011 "Brute force closes the window"); the real token is then
// refused; and once the window is closed a fresh unpinned connection cannot
// even complete the TLS handshake.
func TestRateLimitLockout(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)
	replica := mustIdentity(t, "kitchen-server")
	payload := h.payload(t, w)
	realToken := payload.Token

	for i := 1; i <= devices.MaxPairingFailures; i++ {
		_, err := devices.Pair(context.Background(), client(t, replica, payload.Fingerprint), payload,
			devices.PairRequest{Token: "wrong-token", DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
			&pinningStore{reg: newFakeRegistry()}, time.Now())
		if !errors.Is(err, devices.ErrTokenInvalid) {
			t.Fatalf("attempt %d: %v, want ErrTokenInvalid", i, err)
		}
	}
	if st := w.Status(); st.Open || st.Reason != devices.CloseRateLimited {
		t.Fatalf("window status = %+v, want rate-limit closed", st)
	}

	// Even the real token is refused now — if the connection gets that far:
	// with the window closed, the unpinned handshake itself is rejected, so
	// either failure mode proves the lockout.
	_, err := devices.Pair(context.Background(), client(t, replica, payload.Fingerprint), payload,
		devices.PairRequest{Token: realToken, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&pinningStore{reg: newFakeRegistry()}, time.Now())
	if err == nil {
		t.Fatal("pairing succeeded after rate-limit closure")
	}
	if len(h.store.all()) != 0 {
		t.Errorf("importer pinned %+v after brute force, want none", h.store.all())
	}
}

// TestReplayRejected: after a completed pairing, presenting the consumed
// token again — even from the already-pinned peer, whose certificate still
// handshakes — is refused with token_consumed (SPEC-0011 "Replay
// Resistance": tokens are consumed atomically on first presentation).
func TestReplayRejected(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)
	replica := mustIdentity(t, "kitchen-server")
	payload := h.payload(t, w)

	if _, err := devices.Pair(context.Background(), client(t, replica, payload.Fingerprint), payload,
		devices.PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&pinningStore{reg: newFakeRegistry()}, time.Now()); err != nil {
		t.Fatalf("first Pair: %v", err)
	}

	_, err := devices.Pair(context.Background(), client(t, replica, payload.Fingerprint), payload,
		devices.PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&pinningStore{reg: newFakeRegistry()}, time.Now())
	if !errors.Is(err, devices.ErrTokenConsumed) {
		t.Fatalf("replayed Pair = %v, want ErrTokenConsumed", err)
	}
}

// TestRevocationIsImmediate: unpairing takes effect on the NEXT REQUEST, not
// the next handshake — a pinned peer with a live keep-alive connection loses
// access the moment its registry row is gone (SPEC-0011 "Unpairing and
// Revocation": local, immediate, no cooperation required).
func TestRevocationIsImmediate(t *testing.T) {
	h := startListener(t, nil)
	replica := mustIdentity(t, "kitchen-server")
	h.reg.pin(replica.Fingerprint())

	c := client(t, replica, h.id.Fingerprint()) // keep-alives on: same conn below
	resp, err := c.Get("https://" + h.addr + PingPath)
	if err != nil {
		t.Fatalf("pinned ping: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pinned ping = %d, want 200", resp.StatusCode)
	}

	h.reg.unpin(replica.Fingerprint()) // revoke

	resp, err = c.Get("https://" + h.addr + PingPath)
	if err != nil {
		// Also acceptable: the server killed the cached conn and the fresh
		// handshake was rejected at the TLS layer.
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("post-revocation ping = %d, want 403", resp.StatusCode)
	}
}

// TestNoRedirectsEver: paths that http.ServeMux would canonicalize with a
// 301 (double slashes, trailing slashes) must never produce a 3xx — the sync
// protocol emits no redirects (SPEC-0011 "Redirect Validation").
func TestNoRedirectsEver(t *testing.T) {
	h := startListener(t, nil)
	replica := mustIdentity(t, "kitchen-server")
	h.reg.pin(replica.Fingerprint())
	c := client(t, replica, h.id.Fingerprint())

	for _, path := range []string{"//v1/ping", "/v1/ping/", "/v1/pair/", "/../v1/ping"} {
		req, err := http.NewRequest(http.MethodGet, "https://"+h.addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			// NewPeerClient turns any followed redirect into an error carrying
			// the sentinel; reaching it means the server DID emit a 3xx.
			if errors.Is(err, devices.ErrRedirectResponse) {
				t.Fatalf("GET %s: server emitted a redirect", path)
			}
			t.Fatalf("GET %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			t.Errorf("GET %s = %d, sync protocol must never redirect", path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Errorf("GET %s carried Location: %s", path, loc)
		}
	}
}

// TestServesOnlyTheDevicesAPI: the sync listener never serves the web UI —
// every non-API path is a JSON 404 for a pinned peer, never HTML (SPEC-0011
// "Sync Listener Posture": the listener serves the devices API only).
func TestServesOnlyTheDevicesAPI(t *testing.T) {
	h := startListener(t, nil)
	replica := mustIdentity(t, "kitchen-server")
	h.reg.pin(replica.Fingerprint())
	c := client(t, replica, h.id.Fingerprint())

	for _, path := range []string{"/", "/settings", "/static/app.css", "/search"} {
		resp, err := c.Get("https://" + h.addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (devices API only)", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("GET %s Content-Type = %q, want application/json (never HTML)", path, ct)
		}
		if strings.Contains(strings.ToLower(string(body)), "<html") {
			t.Errorf("GET %s returned HTML on the sync listener", path)
		}
	}
}

// TestShutdownInvalidatesWindow: cancelling the listener's context stops it
// AND invalidates the open pairing window, so a stale QR token cannot
// outlive the listener (SPEC-0011: disabling stops the listener and
// invalidates any open pairing window; REQ "Concurrency Safety" graceful
// shutdown).
func TestShutdownInvalidatesWindow(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)

	// shutdown blocks on Serve's return, which closes the socket — reaching
	// the assertions below proves the worker exited cleanly.
	h.shutdown()
	if st := w.Status(); st.Open || st.Reason != devices.CloseDisabled {
		t.Errorf("window after shutdown = %+v, want closed/disabled", st)
	}
}

// TestPairBodyCap: the pairing endpoint bounds request bodies at 64 KiB via
// http.MaxBytesReader (SPEC-0011 "Request Body Size Limits") — an oversized
// body is refused as a bad request, not read to completion.
func TestPairBodyCap(t *testing.T) {
	w := openWindow(t)
	h := startListener(t, w)
	replica := mustIdentity(t, "kitchen-server")
	c := client(t, replica, h.id.Fingerprint())

	huge := fmt.Sprintf(`{"token":%q,"device_name":"x","listener_addr":"1.2.3.4:1"}`,
		strings.Repeat("A", 128<<10))
	resp, err := c.Post("https://"+h.addr+devices.PairPath, "application/json", strings.NewReader(huge))
	if err != nil {
		t.Fatalf("oversized pair POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized pair body = %d, want 400", resp.StatusCode)
	}
	// The window is untouched by a request that never parsed.
	if st := w.Status(); !st.Open {
		t.Errorf("window closed by an oversized body: %+v", st)
	}
}
