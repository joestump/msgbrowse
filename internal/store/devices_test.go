// Governing: SPEC-0011 REQ "Database Operation Standards" — migration up and
// idempotence for the device-sync state tables, transactional round adoption,
// importer-conflict enforcement, and inert-when-unused guarantees.
package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joestump/msgbrowse/internal/devices"
)

// TestMigrateV9CreatesInertSyncTables covers the v9 migration on a fresh
// database: the tables exist, are empty (inert on nodes that never enable
// device sync), attach no triggers, and re-opening the same database is a
// no-op (idempotence via the user_version guard).
func TestMigrateV9CreatesInertSyncTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v, err := readUserVersion(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}

	for _, table := range []string{"paired_devices", "sync_state"} {
		var n int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migration", table)
		}
		var rows int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("table %s has %d rows on a fresh node, want 0 (inert)", table, rows)
		}
	}

	// No triggers reference the sync tables — they cannot affect nodes that
	// never enable the feature.
	var trig int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger'
		  AND (tbl_name = 'paired_devices' OR tbl_name = 'sync_state')`).Scan(&trig); err != nil {
		t.Fatal(err)
	}
	if trig != 0 {
		t.Errorf("sync tables have %d triggers, want 0", trig)
	}

	// Idempotence: closing and re-opening (which re-runs migrate) is a no-op.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open after v9: %v", err)
	}
	defer st2.Close()
	if v, err := readUserVersion(ctx, st2.DB()); err != nil || v != schemaVersion {
		t.Fatalf("re-open user_version = %d (err %v), want %d", v, err, schemaVersion)
	}
}

// TestMigrateV8ToV9 walks a database stamped at v8 (the previous release)
// through the runner and asserts only the additive v9 change lands.
func TestMigrateV8ToV9(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v8-to-v9.sqlite")
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// Build a v8 database the way a real one was built: v1..v8 in order.
	for v := 1; v <= 8; v++ {
		if _, err := db.ExecContext(ctx, migrations[v]); err != nil {
			t.Fatalf("apply v%d: %v", v, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate v8→v9: %v", err)
	}
	if v, err := readUserVersion(ctx, db); err != nil || v != schemaVersion {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, schemaVersion)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'
		  AND name IN ('paired_devices', 'sync_state')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("sync tables present = %d, want 2", n)
	}
}

func testPeer(name, fp string, roles map[string]devices.Role) devices.Peer {
	return devices.Peer{
		Name:        name,
		Fingerprint: fp,
		Address:     "192.168.1.20:8788",
		Roles:       roles,
		PairedAt:    time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC),
	}
}

// fp64 builds a deterministic 64-char pseudo-fingerprint for tests.
func fp64(seed string) string {
	return strings.Repeat("0", 64-len(seed)) + seed
}

func TestPairedDeviceRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	peer := testPeer("kitchen-server", fp64("a1"), map[string]devices.Role{
		"signal": devices.RoleReplica, "imessage": devices.RoleReplica,
	})
	id, err := st.UpsertPairedDevice(ctx, peer)
	if err != nil {
		t.Fatalf("UpsertPairedDevice: %v", err)
	}
	if id == 0 {
		t.Fatal("UpsertPairedDevice returned id 0")
	}

	got, err := st.GetPairedDeviceByFingerprint(ctx, fp64("a1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != peer.Name || got.Address != peer.Address || !got.PairedAt.Equal(peer.PairedAt) {
		t.Errorf("got %+v, want %+v", got, peer)
	}
	if got.Roles["signal"] != devices.RoleReplica || got.Roles["imessage"] != devices.RoleReplica {
		t.Errorf("roles = %+v, want replica for both sources", got.Roles)
	}

	// Upsert with the same fingerprint updates in place (address/name churn)
	// and never duplicates the peer.
	peer.Name = "kitchen-server-renamed"
	peer.Address = "192.168.1.99:8788"
	id2, err := st.UpsertPairedDevice(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Errorf("re-upsert changed id: %d != %d", id2, id)
	}
	all, err := st.ListPairedDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "kitchen-server-renamed" || all[0].Address != "192.168.1.99:8788" {
		t.Errorf("after re-upsert: %+v, want single updated peer", all)
	}

	// Unknown fingerprint lookups carry the sentinel the TLS layer matches.
	if _, err := st.GetPairedDeviceByFingerprint(ctx, fp64("ff")); !errors.Is(err, devices.ErrUnknownPeerCertificate) {
		t.Errorf("unknown fingerprint error = %v, want ErrUnknownPeerCertificate", err)
	}
}

// TestImporterConflict covers SPEC-0011 "Second importer claim rejected": a
// second peer claiming importer for an already-claimed source fails with
// ErrImporterConflict naming the incumbent; distinct sources coexist; and a
// re-upsert of the SAME peer never conflicts with itself.
func TestImporterConflict(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	incumbent := testPeer("mac-importer", fp64("aa"), map[string]devices.Role{"signal": devices.RoleImporter})
	if _, err := st.UpsertPairedDevice(ctx, incumbent); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		peer     devices.Peer
		wantErr  bool
		wantName string // substring the conflict error must carry
	}{
		{
			name:     "second importer for the same source rejected",
			peer:     testPeer("rogue", fp64("bb"), map[string]devices.Role{"signal": devices.RoleImporter}),
			wantErr:  true,
			wantName: "mac-importer",
		},
		{
			name: "importer for a different source accepted",
			peer: testPeer("whatsapp-box", fp64("cc"), map[string]devices.Role{"whatsapp": devices.RoleImporter}),
		},
		{
			name: "replica role for a claimed source accepted",
			peer: testPeer("replica-box", fp64("dd"), map[string]devices.Role{"signal": devices.RoleReplica}),
		},
		{
			name: "incumbent re-upserts itself without conflict",
			peer: testPeer("mac-importer", fp64("aa"), map[string]devices.Role{"signal": devices.RoleImporter}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := st.UpsertPairedDevice(ctx, tt.peer)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("UpsertPairedDevice: %v", err)
				}
				return
			}
			if !errors.Is(err, devices.ErrImporterConflict) {
				t.Fatalf("error = %v, want ErrImporterConflict", err)
			}
			if !strings.Contains(err.Error(), tt.wantName) {
				t.Errorf("conflict error %q does not name incumbent %q", err, tt.wantName)
			}
		})
	}
}

// TestUnpairRevokesAndCascades: deleting a peer removes the pin and, via
// cascade, its sync_state rows — and nothing else (SPEC-0011 "Unpairing and
// Revocation": only future synchronization is severed).
func TestUnpairRevokesAndCascades(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.UpsertPairedDevice(ctx, testPeer("kitchen-server", fp64("a1"),
		map[string]devices.Role{"signal": devices.RoleImporter}))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitSyncRound(ctx, id, "signal", 3, []TransferCursor{
		{RelPath: "Harper/chat.md", SizeBytes: 10, SHA256: fp64("11"), FetchedBytes: 10, Verified: true},
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeletePairedDevice(ctx, id); err != nil {
		t.Fatalf("DeletePairedDevice: %v", err)
	}
	if _, err := st.GetPairedDeviceByFingerprint(ctx, fp64("a1")); !errors.Is(err, devices.ErrUnknownPeerCertificate) {
		t.Errorf("after unpair, lookup = %v, want ErrUnknownPeerCertificate", err)
	}
	var rows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_state`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("sync_state rows after unpair = %d, want 0 (cascade)", rows)
	}

	// Unpairing an unknown peer is a distinguishable error, not a silent no-op.
	if err := st.DeletePairedDevice(ctx, id); !errors.Is(err, devices.ErrUnknownPeerCertificate) {
		t.Errorf("double unpair = %v, want ErrUnknownPeerCertificate", err)
	}
}

// TestCommitSyncRoundAtomicity covers SPEC-0011 "Round adoption is atomic in
// sync state": generation and cursors commit together; a failed round commit
// leaves BOTH untouched.
func TestCommitSyncRoundAtomicity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.UpsertPairedDevice(ctx, testPeer("mac-importer", fp64("aa"),
		map[string]devices.Role{"signal": devices.RoleImporter}))
	if err != nil {
		t.Fatal(err)
	}

	// Round 1 commits generation + cursors together.
	round1 := []TransferCursor{
		{RelPath: "Harper/chat.md", SizeBytes: 100, SHA256: fp64("01"), FetchedBytes: 100, Verified: true},
		{RelPath: "Harper/media/cabin.jpg", SizeBytes: 2048, SHA256: fp64("02"), FetchedBytes: 2048, Verified: true},
	}
	if err := st.CommitSyncRound(ctx, id, "signal", 1, round1); err != nil {
		t.Fatalf("CommitSyncRound: %v", err)
	}
	gen, err := st.SyncGeneration(ctx, id, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 {
		t.Errorf("generation = %d, want 1", gen)
	}
	cursors, err := st.TransferCursors(ctx, id, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 2 {
		t.Fatalf("cursors = %d, want 2", len(cursors))
	}

	// A round with an invalid cursor fails as a WHOLE: the generation row
	// still says 1 and round 1's cursors survive untouched.
	bad := []TransferCursor{
		{RelPath: "Harper/chat.md", SizeBytes: 120, SHA256: fp64("03"), FetchedBytes: 120, Verified: true},
		{RelPath: "", SizeBytes: 1, SHA256: fp64("04")}, // invalid: reserved rel_path
	}
	if err := st.CommitSyncRound(ctx, id, "signal", 2, bad); err == nil {
		t.Fatal("CommitSyncRound with invalid cursor succeeded, want error")
	}
	gen, err = st.SyncGeneration(ctx, id, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 {
		t.Errorf("generation after failed round = %d, want 1 (rolled back)", gen)
	}
	cursors, err = st.TransferCursors(ctx, id, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 2 || cursors[0].SizeBytes != 100 {
		t.Errorf("cursors after failed round = %+v, want round 1 state intact", cursors)
	}

	// Round 2 replaces the cursor set (dropped files pruned) and advances the
	// generation in the same transaction.
	round2 := []TransferCursor{
		{RelPath: "Harper/chat.md", SizeBytes: 120, SHA256: fp64("03"), FetchedBytes: 64, Verified: false},
	}
	if err := st.CommitSyncRound(ctx, id, "signal", 2, round2); err != nil {
		t.Fatal(err)
	}
	gen, _ = st.SyncGeneration(ctx, id, "signal")
	if gen != 2 {
		t.Errorf("generation = %d, want 2", gen)
	}
	cursors, _ = st.TransferCursors(ctx, id, "signal")
	if len(cursors) != 1 || cursors[0].FetchedBytes != 64 || cursors[0].Verified {
		t.Errorf("cursors = %+v, want the single resumable round-2 cursor", cursors)
	}

	// Generations are per (peer, source): an unrelated source reads 0.
	if gen, err := st.SyncGeneration(ctx, id, "imessage"); err != nil || gen != 0 {
		t.Errorf("imessage generation = %d (err %v), want 0", gen, err)
	}
}

func TestPutTransferCursor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.UpsertPairedDevice(ctx, testPeer("mac-importer", fp64("aa"),
		map[string]devices.Role{"signal": devices.RoleImporter}))
	if err != nil {
		t.Fatal(err)
	}

	// Reserved rel_path refused: '' is the generation row.
	if err := st.PutTransferCursor(ctx, id, "signal", TransferCursor{RelPath: ""}); err == nil {
		t.Error("PutTransferCursor accepted empty rel_path")
	}

	// Progressive updates upsert the same row (resume bookkeeping).
	c := TransferCursor{RelPath: "Harper/media/video.mov", SizeBytes: 4096, SHA256: fp64("05"), FetchedBytes: 1024}
	if err := st.PutTransferCursor(ctx, id, "signal", c); err != nil {
		t.Fatal(err)
	}
	c.FetchedBytes = 4096
	c.Verified = true
	if err := st.PutTransferCursor(ctx, id, "signal", c); err != nil {
		t.Fatal(err)
	}
	got, err := st.TransferCursors(ctx, id, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FetchedBytes != 4096 || !got[0].Verified {
		t.Errorf("cursors = %+v, want one fully fetched verified cursor", got)
	}
}
