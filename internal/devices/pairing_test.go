// Governing: SPEC-0011 REQ "Pairing Acceptance and Mutual Certificate
// Pinning" + REQ "Pairing Initiation" — the full pairing handshake with
// mutual pinning, abort-before-token-disclosure, single-use over the wire,
// and the five-failure closure, all exercised over an in-memory transport
// (net.Pipe conns fed to http.Server/http.Transport): no socket is opened.
package devices

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
)

// memListener is a net.Listener whose connections are net.Pipe ends handed to
// it by dial — an in-memory wire between http.Server and http.Transport.
type memListener struct {
	conns chan net.Conn
	once  sync.Once
	done  chan struct{}
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *memListener) Addr() net.Addr { return memAddr{} }

// dial mints an in-memory conn pair (transport_test.go), hands the server end
// to Accept, and returns the client end.
func (l *memListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := memConnPair()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		client.Close()
		return nil, ctx.Err()
	}
}

// memPeerStore is an in-memory PeerStore recording every upsert.
type memPeerStore struct {
	mu    sync.Mutex
	peers []Peer
}

func (m *memPeerStore) UpsertPairedDevice(_ context.Context, p Peer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.peers {
		if existing.Fingerprint == p.Fingerprint {
			p.ID = existing.ID
			m.peers[i] = p
			return p.ID, nil
		}
	}
	p.ID = int64(len(m.peers) + 1)
	m.peers = append(m.peers, p)
	return p.ID, nil
}

func (m *memPeerStore) all() []Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Peer(nil), m.peers...)
}

// pairingHarness wires an Importer behind an in-memory TLS "listener" and
// returns a dialer factory for replica-side clients.
type pairingHarness struct {
	importerID *Identity
	replicaID  *Identity
	impStore   *memPeerStore
	window     *Window
	importer   *Importer
	ln         *memListener
	handled    chan struct{} // one tick per request that REACHED the handler
}

// newPairingHarness starts the importer's pairing service over the in-memory
// listener. serverID is the identity the SERVER presents (pass an imposter to
// simulate a wrong device / MITM).
func newPairingHarness(t *testing.T, serverID *Identity, opts ...WindowOption) *pairingHarness {
	t.Helper()
	h := &pairingHarness{
		importerID: serverID,
		replicaID:  mustIdentity(t, "kitchen-server"),
		impStore:   &memPeerStore{},
		ln:         newMemListener(),
		handled:    make(chan struct{}, 64),
	}
	var err error
	h.window, err = OpenWindow(0, opts...)
	if err != nil {
		t.Fatal(err)
	}
	h.importer = &Importer{
		DeviceName: "mac-importer",
		Sources:    []string{"signal", "imessage"},
		Store:      h.impStore,
		Window:     h.window,
		Logger:     log.New(io.Discard),
	}

	mux := http.NewServeMux()
	inner := h.importer.PairHandler()
	mux.Handle(PairPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handled <- struct{}{}
		inner.ServeHTTP(w, r)
	}))
	srv := &http.Server{Handler: mux, TLSConfig: serverID.PairingServerTLS()}
	go func() { _ = srv.ServeTLS(h.ln, "", "") }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = h.ln.Close()
	})
	return h
}

// client builds a replica-side http.Client whose transport dials the
// in-memory listener with the given identity, pinned to pinnedFP. The TLS
// handshake happens inside the dial, so a pin mismatch fails before any HTTP
// byte — token included — is written.
func (h *pairingHarness) client(t *testing.T, id *Identity, pinnedFP string) *http.Client {
	t.Helper()
	cfg, err := id.ClientTLS(pinnedFP)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := h.ln.dial(ctx)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(conn, cfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tc, nil
		},
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// handlerSaw reports how many requests reached the pairing handler.
func (h *pairingHarness) handlerSaw() int {
	n := 0
	for {
		select {
		case <-h.handled:
			n++
		default:
			return n
		}
	}
}

func (h *pairingHarness) payload(t *testing.T) *PairingPayload {
	t.Helper()
	p, err := NewPairingPayload("192.168.1.10:8788", h.window.Token(), h.importerID.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPairingFullHandshake runs the whole exchange over the in-memory
// transport: fingerprint verify → token consume → mutual pinning → peer
// persistence on both sides (design.md "Pairing handshake" sequence).
func TestPairingFullHandshake(t *testing.T) {
	importerID := mustIdentity(t, "mac-importer")
	h := newPairingHarness(t, importerID)
	payload := h.payload(t)
	replicaStore := &memPeerStore{}
	now := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC)

	peer, err := Pair(context.Background(),
		h.client(t, h.replicaID, payload.Fingerprint),
		payload,
		PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		replicaStore, now)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}

	// Replica side pinned the importer.
	if peer.Fingerprint != importerID.Fingerprint() {
		t.Errorf("replica pinned %s, want importer fingerprint %s", peer.Fingerprint, importerID.Fingerprint())
	}
	if peer.Name != "mac-importer" || peer.Address != payload.Endpoint {
		t.Errorf("peer = %+v, want importer name/address", peer)
	}
	for _, src := range []string{"signal", "imessage"} {
		if peer.Roles[src] != RoleImporter {
			t.Errorf("peer role for %s = %q, want importer", src, peer.Roles[src])
		}
	}
	if got := replicaStore.all(); len(got) != 1 || got[0].Fingerprint != importerID.Fingerprint() {
		t.Errorf("replica store = %+v, want the pinned importer", got)
	}

	// Importer side pinned the replica's CLIENT certificate from the TLS
	// handshake — mutual pinning, both directions.
	impPeers := h.impStore.all()
	if len(impPeers) != 1 {
		t.Fatalf("importer store has %d peers, want 1", len(impPeers))
	}
	got := impPeers[0]
	if got.Fingerprint != h.replicaID.Fingerprint() {
		t.Errorf("importer pinned %s, want replica fingerprint %s", got.Fingerprint, h.replicaID.Fingerprint())
	}
	if got.Name != "kitchen-server" || got.Address != "192.168.1.20:8788" {
		t.Errorf("importer peer = %+v, want replica name/address", got)
	}
	for _, src := range []string{"signal", "imessage"} {
		if got.Roles[src] != RoleReplica {
			t.Errorf("importer records %s role %q, want replica", src, got.Roles[src])
		}
	}

	// The window is consumed and closed.
	if st := h.window.Status(); st.Open || st.Reason != CloseConsumed {
		t.Errorf("window status = %+v, want closed/consumed", st)
	}
}

// TestPairingTokenSingleUseOverTransport: a second device presenting the
// SAME token after a completed pairing is rejected (SPEC-0011 "Token is
// single-use"); pairing another device requires a new window.
func TestPairingTokenSingleUseOverTransport(t *testing.T) {
	importerID := mustIdentity(t, "mac-importer")
	h := newPairingHarness(t, importerID)
	payload := h.payload(t)

	if _, err := Pair(context.Background(),
		h.client(t, h.replicaID, payload.Fingerprint), payload,
		PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&memPeerStore{}, time.Now()); err != nil {
		t.Fatalf("first Pair: %v", err)
	}

	secondID := mustIdentity(t, "second-device")
	_, err := Pair(context.Background(),
		h.client(t, secondID, payload.Fingerprint), payload,
		PairRequest{Token: payload.Token, DeviceName: "second-device", ListenerAddr: "192.168.1.30:8788"},
		&memPeerStore{}, time.Now())
	if !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("second Pair = %v, want ErrTokenConsumed", err)
	}

	// Only the first device is pinned.
	impPeers := h.impStore.all()
	if len(impPeers) != 1 || impPeers[0].Name != "kitchen-server" {
		t.Errorf("importer store = %+v, want only the first device", impPeers)
	}
}

// TestPairingAbortsBeforeTokenDisclosure: the server presents a certificate
// that does not match the payload fingerprint (wrong device / MITM). The
// replica must abort during the TLS handshake — the pairing request carrying
// the token is never sent (SPEC-0011 "Fingerprint mismatch aborts before
// token disclosure").
func TestPairingAbortsBeforeTokenDisclosure(t *testing.T) {
	imposterID := mustIdentity(t, "imposter")
	h := newPairingHarness(t, imposterID) // server presents the imposter cert

	// The payload advertises the REAL importer's fingerprint.
	realImporter := mustIdentity(t, "mac-importer")
	payload, err := NewPairingPayload("192.168.1.10:8788", h.window.Token(), realImporter.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}

	_, err = Pair(context.Background(),
		h.client(t, h.replicaID, payload.Fingerprint), payload,
		PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&memPeerStore{}, time.Now())
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Pair = %v, want ErrFingerprintMismatch", err)
	}

	// The token was never transmitted: no request reached the handler, the
	// token was never presented to the window (zero failures, still open),
	// and nothing was pinned.
	if n := h.handlerSaw(); n != 0 {
		t.Errorf("pairing handler saw %d request(s), want 0", n)
	}
	if st := h.window.Status(); !st.Open || st.Failures != 0 {
		t.Errorf("window status = %+v, want untouched (open, 0 failures)", st)
	}
	if peers := h.impStore.all(); len(peers) != 0 {
		t.Errorf("importer store = %+v, want empty", peers)
	}
}

// TestPairingFiveFailuresCloseWindow drives SPEC-0011's "Brute force closes
// the window" over the transport: five bad tokens close the window; the
// correct token is then refused until a new window opens.
func TestPairingFiveFailuresCloseWindow(t *testing.T) {
	importerID := mustIdentity(t, "mac-importer")
	h := newPairingHarness(t, importerID)
	payload := h.payload(t)
	realToken := payload.Token

	for i := 1; i <= MaxPairingFailures; i++ {
		_, err := Pair(context.Background(),
			h.client(t, h.replicaID, payload.Fingerprint), payload,
			PairRequest{Token: "wrong-token", DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
			&memPeerStore{}, time.Now())
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("attempt %d: Pair = %v, want ErrTokenInvalid", i, err)
		}
	}
	if st := h.window.Status(); st.Open || st.Reason != CloseRateLimited {
		t.Fatalf("window status = %+v, want rate-limit closed", st)
	}

	// Even the real token is refused now.
	_, err := Pair(context.Background(),
		h.client(t, h.replicaID, payload.Fingerprint), payload,
		PairRequest{Token: realToken, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&memPeerStore{}, time.Now())
	if !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("post-closure Pair = %v, want ErrWindowClosed", err)
	}
	if peers := h.impStore.all(); len(peers) != 0 {
		t.Errorf("importer store = %+v, want empty after brute force", peers)
	}
}

// TestPairingExpiredTokenOverTransport: a token presented after TTL expiry on
// the ISSUER's clock is rejected with a clear error and the window closes
// (SPEC-0011 "Expired token").
func TestPairingExpiredTokenOverTransport(t *testing.T) {
	importerID := mustIdentity(t, "mac-importer")
	clock := newFakeClock()
	h := newPairingHarness(t, importerID, WithClock(clock.Now))
	payload := h.payload(t)

	clock.Advance(DefaultTokenTTL + time.Minute)

	_, err := Pair(context.Background(),
		h.client(t, h.replicaID, payload.Fingerprint), payload,
		PairRequest{Token: payload.Token, DeviceName: "kitchen-server", ListenerAddr: "192.168.1.20:8788"},
		&memPeerStore{}, time.Now())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Pair = %v, want ErrTokenExpired", err)
	}
	if st := h.window.Status(); st.Open || st.Reason != CloseExpired {
		t.Errorf("window status = %+v, want closed/expired", st)
	}
}

// TestPairHandlerEdgeCases exercises the handler directly (no transport):
// wrong method, no window ever opened, and oversized bodies.
func TestPairHandlerEdgeCases(t *testing.T) {
	imp := &Importer{
		DeviceName: "mac-importer",
		Sources:    []string{"signal"},
		Store:      &memPeerStore{},
		Window:     nil, // never opened
		Logger:     log.New(io.Discard),
	}
	handler := imp.PairHandler()

	t.Run("GET rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PairPath, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET status = %d, want 405", rec.Code)
		}
	})

	t.Run("request without TLS client cert rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, PairPath, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("no-TLS status = %d, want 400", rec.Code)
		}
	})
}
