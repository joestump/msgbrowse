// Governing: ADR-0018 (pairing establishes mutual pinned trust), SPEC-0011
// REQ "Pairing Acceptance and Mutual Certificate Pinning" — on a valid token
// the importer pins the replica's client-certificate fingerprint and persists
// the peer record; the replica pins the importer's fingerprint symmetrically.
// SPEC-0011 REQ "Error Handling Standards" — structured logging, wrapped
// errors, no silent swallowing.
package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charmbracelet/log"
)

// PairPath is the pairing endpoint path (SPEC-0011 Authentication table).
// The listener story mounts Importer.PairHandler here; this package only
// defines the exchange.
const PairPath = "/v1/pair"

// maxPairBodyBytes bounds the pairing request body (SPEC-0011 "Request Body
// Size Limits": all sync request bodies are small JSON, capped at 64 KiB).
const maxPairBodyBytes = 64 << 10

// Role is a peer's per-source role in the sync topology (ADR-0018): the node
// that runs a source's exporters is its importer; everyone else replicates.
type Role string

const (
	// RoleImporter marks the peer as the producer of a source's archive.
	RoleImporter Role = "importer"
	// RoleReplica marks the peer as a consumer of a source's archive.
	RoleReplica Role = "replica"
)

// Peer is a paired device as persisted in the paired_devices registry:
// name, pinned certificate fingerprint, last-known listener address, and the
// per-source roles the peer plays from this node's point of view.
type Peer struct {
	// ID is the registry rowid (0 before first persistence).
	ID int64
	// Name is the peer's self-reported device name.
	Name string
	// Fingerprint is the canonical SHA-256 fingerprint of the peer's
	// certificate, pinned at pairing (see Fingerprint).
	Fingerprint string
	// Address is the peer's sync listener as host:port, updated as the peer
	// re-announces (mDNS healing lands with the listener story).
	Address string
	// Roles maps source name → the role the PEER plays for that source.
	Roles map[string]Role
	// PairedAt is when pairing completed, on this node's clock.
	PairedAt time.Time
}

// PeerStore is the slice of persistence the pairing exchange needs. It is
// implemented by *store.Store (paired_devices + sync_state tables) and by
// in-memory fakes in tests.
type PeerStore interface {
	// UpsertPairedDevice persists a peer keyed by fingerprint, returning its
	// registry ID. It must fail with ErrImporterConflict if the peer claims
	// RoleImporter for a source another registered peer already imports.
	UpsertPairedDevice(ctx context.Context, p Peer) (int64, error)
}

// PairRequest is the JSON body the replica POSTs to PairPath over the
// fingerprint-verified TLS channel. The replica's certificate travels in the
// TLS handshake itself, not in the body.
type PairRequest struct {
	// Token is the single-use pairing token from the QR/manual payload.
	Token string `json:"token"`
	// DeviceName is the replica's self-reported name.
	DeviceName string `json:"device_name"`
	// ListenerAddr is the replica's own sync listener host:port, persisted as
	// the peer address so the importer can notify it after ingest passes.
	ListenerAddr string `json:"listener_addr"`
}

// PairResponse is the importer's JSON reply on successful pairing: what the
// replica needs to persist its side of the peer record.
type PairResponse struct {
	// DeviceName is the importer's device name.
	DeviceName string `json:"device_name"`
	// Sources are the source names this importer serves (it holds
	// RoleImporter for each, from the replica's point of view).
	Sources []string `json:"sources"`
}

// pairError is the JSON error body; Code is one of the stable strings below
// so the replica can render a precise message without parsing prose.
type pairError struct {
	Code string `json:"error"`
}

// Stable pairing error codes carried in pairError.Code.
const (
	PairErrTokenExpired  = "token_expired"
	PairErrTokenConsumed = "token_consumed"
	PairErrTokenInvalid  = "token_invalid"
	PairErrWindowClosed  = "window_closed"
	PairErrBadRequest    = "bad_request"
	PairErrInternal      = "internal"
)

// Importer is the importer-side pairing service: it owns which window is
// current and turns a valid token presentation into a pinned, persisted peer.
// It is transport-agnostic — PairHandler is a plain http.Handler the listener
// story will mount at PairPath behind Identity.PairingServerTLS.
type Importer struct {
	// DeviceName is this importer's name, returned to replicas.
	DeviceName string
	// Sources are the sources this node imports (ADR-0018 roles).
	Sources []string
	// Store persists pinned peers.
	Store PeerStore
	// Window is the current pairing window (nil when none was ever opened;
	// requests are then refused with PairErrWindowClosed).
	Window *Window
	// Logger receives structured pairing events; nil uses log.Default().
	Logger *log.Logger
	// Now overrides the clock in tests; nil uses time.Now.
	Now func() time.Time
}

func (im *Importer) logger() *log.Logger {
	if im.Logger != nil {
		return im.Logger
	}
	return log.Default()
}

func (im *Importer) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

// PairHandler serves POST PairPath. The connection is expected to arrive
// through Identity.PairingServerTLS, so the replica's client certificate is
// present in the request's TLS state; the token is the sole gate (SPEC-0011
// Authentication). On success the replica's fingerprint is pinned and the
// peer persisted as RoleReplica for every source this importer serves —
// atomically with token consumption from the caller's point of view: the
// token is only marked consumed after Consume succeeds, and a persistence
// failure is returned as an error (the operator re-opens a window; a
// half-paired peer cannot connect to anything because nothing was pinned).
func (im *Importer) PairHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, pairError{Code: PairErrBadRequest})
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			// Cannot pin a peer that presented no certificate; the TLS config
			// requires one, so this only fires when mounted incorrectly.
			im.logger().Error("pairing request without client certificate", "remote", r.RemoteAddr)
			writeJSON(w, http.StatusBadRequest, pairError{Code: PairErrBadRequest})
			return
		}

		var req PairRequest
		body := http.MaxBytesReader(w, r.Body, maxPairBodyBytes)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, pairError{Code: PairErrBadRequest})
			return
		}

		fp := Fingerprint(r.TLS.PeerCertificates[0].Raw)

		if im.Window == nil {
			im.logger().Warn("pairing attempt with no open window", "peer_fingerprint", fp)
			writeJSON(w, http.StatusForbidden, pairError{Code: PairErrWindowClosed})
			return
		}
		if err := im.Window.Consume(req.Token); err != nil {
			st := im.Window.Status()
			im.logger().Warn("pairing token rejected",
				"err", err,
				"peer_fingerprint", fp,
				"failures", st.Failures,
				"window_open", st.Open,
				"close_reason", st.Reason,
			)
			switch {
			case errors.Is(err, ErrTokenExpired):
				writeJSON(w, http.StatusForbidden, pairError{Code: PairErrTokenExpired})
			case errors.Is(err, ErrTokenConsumed):
				writeJSON(w, http.StatusForbidden, pairError{Code: PairErrTokenConsumed})
			case errors.Is(err, ErrWindowClosed):
				writeJSON(w, http.StatusForbidden, pairError{Code: PairErrWindowClosed})
			default:
				writeJSON(w, http.StatusForbidden, pairError{Code: PairErrTokenInvalid})
			}
			return
		}

		// Token accepted: pin the replica. From this node's view the new peer
		// is a replica of every source we import.
		roles := make(map[string]Role, len(im.Sources))
		for _, src := range im.Sources {
			roles[src] = RoleReplica
		}
		peer := Peer{
			Name:        req.DeviceName,
			Fingerprint: fp,
			Address:     req.ListenerAddr,
			Roles:       roles,
			PairedAt:    im.now(),
		}
		if _, err := im.Store.UpsertPairedDevice(r.Context(), peer); err != nil {
			im.logger().Error("pairing succeeded but peer persistence failed",
				"err", err, "peer", req.DeviceName, "peer_fingerprint", fp)
			writeJSON(w, http.StatusInternalServerError, pairError{Code: PairErrInternal})
			return
		}

		im.logger().Info("device paired",
			"peer", req.DeviceName,
			"peer_fingerprint", fp,
			"peer_addr", req.ListenerAddr,
		)
		writeJSON(w, http.StatusOK, PairResponse{DeviceName: im.DeviceName, Sources: im.Sources})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Pair runs the replica side of the pairing exchange against a decoded
// payload. The caller supplies the http.Client whose transport dials the
// importer with Identity.ClientTLS(payload.Fingerprint) — so a fingerprint
// mismatch fails the TLS handshake and this function returns
// ErrFingerprintMismatch without the token (in the request body) ever having
// been transmitted. On success it pins payload.Fingerprint by persisting the
// importer as a peer (RoleImporter for every source it serves) in store and
// returns the peer. now stamps PairedAt (this node's clock).
func Pair(ctx context.Context, client *http.Client, payload *PairingPayload, req PairRequest, store PeerStore, now time.Time) (*Peer, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("devices: encode pair request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+payload.Endpoint+PairPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("devices: build pair request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		// Surface the pinning sentinel when the handshake was the failure:
		// this is the abort-before-token-disclosure path.
		if errors.Is(err, ErrFingerprintMismatch) {
			return nil, fmt.Errorf("devices: pairing to %s: possible wrong device or MITM: %w", payload.Endpoint, ErrFingerprintMismatch)
		}
		return nil, fmt.Errorf("devices: pair with %s: %w", payload.Endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var pe pairError
		_ = json.NewDecoder(io.LimitReader(resp.Body, maxPairBodyBytes)).Decode(&pe)
		return nil, fmt.Errorf("devices: pairing rejected by %s: %w", payload.Endpoint, pairErrorToSentinel(pe.Code))
	}

	var pr PairResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPairBodyBytes)).Decode(&pr); err != nil {
		return nil, fmt.Errorf("devices: decode pair response: %w", err)
	}

	roles := make(map[string]Role, len(pr.Sources))
	for _, src := range pr.Sources {
		roles[src] = RoleImporter
	}
	peer := &Peer{
		Name:        pr.DeviceName,
		Fingerprint: payload.Fingerprint,
		Address:     payload.Endpoint,
		Roles:       roles,
		PairedAt:    now,
	}
	id, err := store.UpsertPairedDevice(ctx, *peer)
	if err != nil {
		return nil, fmt.Errorf("devices: persist importer peer %s: %w", pr.DeviceName, err)
	}
	peer.ID = id
	return peer, nil
}

// pairErrorToSentinel maps a wire error code back to the package sentinel so
// replica-side callers can errors.Is against the same values the importer
// used.
func pairErrorToSentinel(code string) error {
	switch code {
	case PairErrTokenExpired:
		return ErrTokenExpired
	case PairErrTokenConsumed:
		return ErrTokenConsumed
	case PairErrWindowClosed:
		return ErrWindowClosed
	case PairErrTokenInvalid:
		return ErrTokenInvalid
	default:
		return fmt.Errorf("unexpected pairing error code %q", code)
	}
}
