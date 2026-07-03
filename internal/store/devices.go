// Governing: ADR-0018 (node-local sync state, never synchronized), SPEC-0011
// REQ "Database Operation Standards" — transactions for multi-step mutations,
// parameterized queries only, explicit connection lifecycle — and REQ
// "Importer and Replica Roles" — a second importer claim for a claimed source
// fails with an error naming the incumbent.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/joestump/msgbrowse/internal/devices"
)

// TransferCursor is one file's durable transfer bookkeeping for a
// (peer, source) pair: the manifest-declared size and hash, the resumable
// byte offset, and whether the staged file has hash-verified.
type TransferCursor struct {
	RelPath      string
	SizeBytes    int64
	SHA256       string
	FetchedBytes int64
	Verified     bool
}

// UpsertPairedDevice inserts or updates a peer keyed by its pinned
// certificate fingerprint and returns the registry ID. It implements
// devices.PeerStore.
//
// The whole operation — importer-conflict check plus write — runs in one
// IMMEDIATE transaction so two concurrent pairings cannot both claim
// importer for the same source. A claim of devices.RoleImporter for a source
// another peer already imports fails with devices.ErrImporterConflict naming
// the incumbent (SPEC-0011 "Second importer claim rejected").
func (s *Store) UpsertPairedDevice(ctx context.Context, p devices.Peer) (int64, error) {
	if p.Fingerprint == "" {
		return 0, fmt.Errorf("upsert paired device: empty fingerprint")
	}
	rolesJSON, err := json.Marshal(p.Roles)
	if err != nil {
		return 0, fmt.Errorf("upsert paired device: encode roles: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("upsert paired device: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Enforce single-importer-per-source against every OTHER registered peer.
	if err := checkImporterConflict(ctx, tx, p); err != nil {
		return 0, err
	}

	pairedAt := p.PairedAt.UTC().Format(time.RFC3339)
	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO paired_devices (name, fingerprint, address, roles, paired_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(fingerprint) DO UPDATE SET
    name    = excluded.name,
    address = excluded.address,
    roles   = excluded.roles
RETURNING id`, p.Name, p.Fingerprint, p.Address, string(rolesJSON), pairedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert paired device %s: %w", p.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("upsert paired device: commit: %w", err)
	}
	return id, nil
}

// checkImporterConflict scans the registry (tiny by construction: a handful
// of devices) for another peer already registered as importer for any source
// p claims to import.
func checkImporterConflict(ctx context.Context, tx *sql.Tx, p devices.Peer) error {
	claimed := make(map[string]bool)
	for src, role := range p.Roles {
		if role == devices.RoleImporter {
			claimed[src] = true
		}
	}
	if len(claimed) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT name, fingerprint, roles FROM paired_devices WHERE fingerprint <> ?`, p.Fingerprint)
	if err != nil {
		return fmt.Errorf("importer conflict check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, fp, rolesJSON string
		if err := rows.Scan(&name, &fp, &rolesJSON); err != nil {
			return fmt.Errorf("importer conflict check scan: %w", err)
		}
		var roles map[string]devices.Role
		if err := json.Unmarshal([]byte(rolesJSON), &roles); err != nil {
			return fmt.Errorf("importer conflict check: decode roles for %s: %w", name, err)
		}
		for src, role := range roles {
			if role == devices.RoleImporter && claimed[src] {
				return fmt.Errorf("source %q already imported by %q (%s): %w",
					src, name, fp, devices.ErrImporterConflict)
			}
		}
	}
	return rows.Err()
}

// GetPairedDeviceByFingerprint looks a peer up by its pinned fingerprint —
// the TLS layer's question ("is this certificate pinned?"). Returns
// devices.ErrUnknownPeerCertificate (wrapped, with the fingerprint) when no
// peer matches.
func (s *Store) GetPairedDeviceByFingerprint(ctx context.Context, fingerprint string) (*devices.Peer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, fingerprint, address, roles, paired_at
  FROM paired_devices WHERE fingerprint = ?`, fingerprint)
	p, err := scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("fingerprint %s: %w", fingerprint, devices.ErrUnknownPeerCertificate)
	}
	return p, err
}

// ListPairedDevices returns every registered peer, ordered by pairing time
// (status surfaces build on this).
func (s *Store) ListPairedDevices(ctx context.Context) ([]devices.Peer, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, fingerprint, address, roles, paired_at
  FROM paired_devices ORDER BY paired_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list paired devices: %w", err)
	}
	defer rows.Close()
	var peers []devices.Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("list paired devices: %w", err)
		}
		peers = append(peers, *p)
	}
	return peers, rows.Err()
}

// DeletePairedDevice unpairs: it removes the peer record (revoking the pin —
// the TLS layer rejects the certificate from the next handshake) and, via
// ON DELETE CASCADE, that peer's sync_state rows. Archive files and the
// database are untouched (SPEC-0011 "Unpairing and Revocation"). Returns
// devices.ErrUnknownPeerCertificate if no such peer exists.
func (s *Store) DeletePairedDevice(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM paired_devices WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete paired device %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete paired device %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("paired device %d: %w", id, devices.ErrUnknownPeerCertificate)
	}
	return nil
}

// scanner abstracts *sql.Row / *sql.Rows for scanPeer.
type scanner interface{ Scan(dest ...any) error }

func scanPeer(sc scanner) (*devices.Peer, error) {
	var p devices.Peer
	var rolesJSON, pairedAt string
	if err := sc.Scan(&p.ID, &p.Name, &p.Fingerprint, &p.Address, &rolesJSON, &pairedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rolesJSON), &p.Roles); err != nil {
		return nil, fmt.Errorf("decode roles for peer %s: %w", p.Name, err)
	}
	t, err := time.Parse(time.RFC3339, pairedAt)
	if err != nil {
		return nil, fmt.Errorf("parse paired_at for peer %s: %w", p.Name, err)
	}
	p.PairedAt = t
	return &p, nil
}

// PutTransferCursor upserts one file's transfer cursor for (peerID, source):
// the resumable per-file progress row written as a fetch streams and when a
// staged file verifies. rel_path must be non-empty — the empty string is
// reserved for the generation row.
func (s *Store) PutTransferCursor(ctx context.Context, peerID int64, source string, c TransferCursor) error {
	if c.RelPath == "" {
		return fmt.Errorf("put transfer cursor: rel_path must not be empty")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sync_state (peer_id, source, rel_path, size_bytes, sha256, fetched_bytes, verified, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(peer_id, source, rel_path) DO UPDATE SET
    size_bytes    = excluded.size_bytes,
    sha256        = excluded.sha256,
    fetched_bytes = excluded.fetched_bytes,
    verified      = excluded.verified,
    updated_at    = excluded.updated_at`,
		peerID, source, c.RelPath, c.SizeBytes, c.SHA256, c.FetchedBytes, boolToInt(c.Verified),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("put transfer cursor %s/%s: %w", source, c.RelPath, err)
	}
	return nil
}

// TransferCursors returns the per-file cursors for (peerID, source), ordered
// by path.
func (s *Store) TransferCursors(ctx context.Context, peerID int64, source string) ([]TransferCursor, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT rel_path, size_bytes, sha256, fetched_bytes, verified
  FROM sync_state
 WHERE peer_id = ? AND source = ? AND rel_path <> ''
 ORDER BY rel_path`, peerID, source)
	if err != nil {
		return nil, fmt.Errorf("transfer cursors %d/%s: %w", peerID, source, err)
	}
	defer rows.Close()
	var out []TransferCursor
	for rows.Next() {
		var c TransferCursor
		var verified int
		if err := rows.Scan(&c.RelPath, &c.SizeBytes, &c.SHA256, &c.FetchedBytes, &verified); err != nil {
			return nil, fmt.Errorf("transfer cursors scan: %w", err)
		}
		c.Verified = verified != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// SyncGeneration returns the last manifest generation fully adopted from
// (peerID, source), or 0 when no round has completed.
func (s *Store) SyncGeneration(ctx context.Context, peerID int64, source string) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx, `
SELECT generation FROM sync_state
 WHERE peer_id = ? AND source = ? AND rel_path = ''`, peerID, source).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sync generation %d/%s: %w", peerID, source, err)
	}
	return gen, nil
}

// CommitSyncRound atomically records a completed round: the new manifest
// generation and the round's final per-file cursors commit in ONE
// transaction, so a crash between them cannot record the generation as
// complete with stale cursors (SPEC-0011 "Round adoption is atomic in sync
// state"). Cursor rows for files no longer in the manifest are pruned in the
// same transaction — they are bookkeeping for transfers that can no longer
// happen.
func (s *Store) CommitSyncRound(ctx context.Context, peerID int64, source string, generation int64, cursors []TransferCursor) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit sync round: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC().Format(time.RFC3339)

	// Reset this (peer, source)'s file rows to exactly the round's cursors.
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM sync_state WHERE peer_id = ? AND source = ? AND rel_path <> ''`,
		peerID, source); err != nil {
		return fmt.Errorf("commit sync round: clear cursors: %w", err)
	}
	for _, c := range cursors {
		if c.RelPath == "" {
			return fmt.Errorf("commit sync round: cursor rel_path must not be empty")
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO sync_state (peer_id, source, rel_path, size_bytes, sha256, fetched_bytes, verified, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			peerID, source, c.RelPath, c.SizeBytes, c.SHA256, c.FetchedBytes, boolToInt(c.Verified), now); err != nil {
			return fmt.Errorf("commit sync round: cursor %s: %w", c.RelPath, err)
		}
	}

	// Record the adopted generation (the rel_path='' row).
	if _, err = tx.ExecContext(ctx, `
INSERT INTO sync_state (peer_id, source, rel_path, generation, updated_at)
VALUES (?, ?, '', ?, ?)
ON CONFLICT(peer_id, source, rel_path) DO UPDATE SET
    generation = excluded.generation,
    updated_at = excluded.updated_at`,
		peerID, source, generation, now); err != nil {
		return fmt.Errorf("commit sync round: generation: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit sync round: commit: %w", err)
	}
	return nil
}
