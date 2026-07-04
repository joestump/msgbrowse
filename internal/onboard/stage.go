// Staging + atomic-adopt: the SPEC-0013 REQ "Error Handling Standards"
// guarantee that a cancelled or failed export never corrupts the managed archive
// root. The exporter always writes into a fresh staging directory beside the
// managed root; only a clean success promotes staging into place, and the
// promotion is a single atomic rename after the previous managed contents are
// swapped aside. A crash, a non-zero exporter, or a Cancel leaves the managed
// root exactly as it was and discards the staging tree.
//
// Governing: SPEC-0013 REQ "Error Handling Standards" ("exports MUST write to a
// staging location and be promoted to the managed root only on success"),
// §Security "No arbitrary paths — managed roots only" (every path here is
// derived from the app-computed managed root, never client input).
package onboard

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joestump/msgbrowse/internal/ingest"
	"github.com/joestump/msgbrowse/internal/whatsapp"
)

// signalExportSubdir is <dest>/export, sigexport's positional destination, kept
// in lockstep with ingest.ExportDir (the layout the importer scans).
func signalExportSubdir(dest string) string {
	return filepath.Join(dest, ingest.ExportDir)
}

// whatsappResultFile is <dest>/result.json, wtsexporter's -j destination, kept
// in lockstep with whatsapp.ResultFile.
func whatsappResultFile(dest string) string {
	return filepath.Join(dest, whatsapp.ResultFile)
}

// stagingSuffix and adoptedTrashSuffix name the sibling directories staging uses
// beside the managed root. They live under the managed root's PARENT (the
// per-source archives dir) so the atomic rename is same-filesystem (rename
// across filesystems is not atomic and can EXDEV).
const (
	stagingSuffix = ".staging"
	trashSuffix   = ".old"
)

// newStaging creates a fresh, empty staging directory beside the managed root
// and returns its path. Any leftover staging tree from a previously crashed run
// is removed first, so a stale partial export can never be adopted. The dir is
// created owner-only (0700) like the managed roots — it holds the same personal
// message data mid-flight.
func newStaging(managedRoot string) (string, error) {
	staging := managedRoot + stagingSuffix
	// Discard any leftover from a prior interrupted run before re-creating.
	if err := os.RemoveAll(staging); err != nil {
		return "", fmt.Errorf("clear stale staging %s: %w", staging, err)
	}
	if err := os.MkdirAll(staging, managedDirPerm); err != nil {
		return "", fmt.Errorf("create staging %s: %w", staging, err)
	}
	return staging, nil
}

// managedDirPerm mirrors internal/setup's managed-dir mode: owner-only (0700),
// because these directories hold the user's exported personal messages and must
// not be world- or group-readable.
const managedDirPerm = 0o700

// discardStaging removes a staging directory, used to clean up after a failed or
// cancelled export so no partial output survives. Errors are returned so the
// caller can log them, but a discard failure never changes the managed root.
func discardStaging(staging string) error {
	if staging == "" {
		return nil
	}
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("discard staging %s: %w", staging, err)
	}
	return nil
}

// adopt promotes a successfully-staged export into the managed archive root
// atomically. It is the only operation that mutates the managed root, and it is
// crash-safe: the previous managed contents (if any) are moved to a sibling
// trash dir first so the rename into place has a clear target, then the trash is
// removed. If the promotion rename fails, the previous contents are restored so
// the managed root is never left empty or half-populated.
//
// The managed root's parent is created if absent (it is <data_dir>/archives,
// which internal/setup.Provision normally makes; adopt is defensive so a first
// Enable before Provision still works).
func adopt(staging, managedRoot string) error {
	parent := filepath.Dir(managedRoot)
	if err := os.MkdirAll(parent, managedDirPerm); err != nil {
		return fmt.Errorf("ensure archive parent %s: %w", parent, err)
	}

	trash := managedRoot + trashSuffix
	// Clear any leftover trash from a prior interrupted adopt.
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("clear stale adopt-trash %s: %w", trash, err)
	}

	// Move the existing managed root aside (if it exists) so the staging rename
	// lands on a clean target. A missing root is fine — a first Enable.
	movedAside := false
	if _, err := os.Stat(managedRoot); err == nil {
		if err := os.Rename(managedRoot, trash); err != nil {
			return fmt.Errorf("move managed root aside: %w", err)
		}
		movedAside = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat managed root %s: %w", managedRoot, err)
	}

	// Promote staging into place. On failure, restore the moved-aside contents so
	// the managed root is never left missing.
	if err := os.Rename(staging, managedRoot); err != nil {
		if movedAside {
			if rerr := os.Rename(trash, managedRoot); rerr != nil {
				return fmt.Errorf("promote staging failed (%v) AND restore failed: %w", err, rerr)
			}
		}
		return fmt.Errorf("promote staging into managed root: %w", err)
	}

	// Success: drop the old contents. A trash-removal failure is non-fatal — the
	// managed root is already correct — but is returned so the caller can log it.
	if movedAside {
		if err := os.RemoveAll(trash); err != nil {
			return fmt.Errorf("adopt succeeded but removing old contents failed: %w", err)
		}
	}
	return nil
}
