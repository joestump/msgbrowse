// Governing: ADR-0020 (the app owns the data dir AND the managed archive roots;
// the user never names a path), SPEC-0013 REQ "App-owned, hidden data and
// archive roots".
package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joestump/msgbrowse/internal/source"
)

// archivesDir is the fixed subdirectory of data_dir that holds the per-source
// managed archive roots. The full managed root for a source is
// <data_dir>/archives/<source>.
const archivesDir = "archives"

// managedDirPerm is the permission for the managed data and archive
// directories. It matches the desktop shell's data-dir mode (0700, owner-only):
// these hold the user's exported personal messages and must not be world- or
// group-readable.
const managedDirPerm = 0o700

// ManagedRoot returns the app-computed managed archive root for a source:
// <dataDir>/archives/<source>. The source MUST be one of the fixed enum values
// (source.Signal, source.IMessage, source.WhatsApp); this is the app-owned
// mapping that keeps a client-supplied string from ever reaching a filesystem
// path (SPEC-0013 "No arbitrary paths — managed roots only"). An unknown source
// or an empty dataDir is an error, never a guessed path.
func ManagedRoot(dataDir, src string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("setup: data dir is empty")
	}
	if !source.IsKnown(src) {
		return "", fmt.Errorf("setup: unknown source %q", src)
	}
	return filepath.Join(dataDir, archivesDir, src), nil
}

// ManagedLayout is the resolved set of app-owned paths for a data_dir: the data
// dir itself and the managed archive root per source, keyed by source id. It is
// what the About/Advanced view (SPEC-0013) shows as "discoverable but never
// required input", and what the provisioner creates.
type ManagedLayout struct {
	// DataDir is the app-owned data directory (anchored to
	// os.UserConfigDir()/msgbrowse by the desktop shell, SPEC-0010).
	DataDir string
	// ArchivesDir is <DataDir>/archives, the parent of every managed root.
	ArchivesDir string
	// Roots maps each source id (source.Signal, source.IMessage,
	// source.WhatsApp) to its managed archive root <DataDir>/archives/<source>.
	Roots map[string]string
}

// ComputeLayout resolves the managed layout for a data_dir without touching the
// filesystem (pure computation — Provision does the mkdir). An empty dataDir is
// an error so a mis-anchored layout can never point at the filesystem root.
func ComputeLayout(dataDir string) (ManagedLayout, error) {
	if dataDir == "" {
		return ManagedLayout{}, fmt.Errorf("setup: data dir is empty")
	}
	roots := make(map[string]string, len(source.All))
	for _, src := range source.All {
		root, err := ManagedRoot(dataDir, src)
		if err != nil {
			return ManagedLayout{}, err
		}
		roots[src] = root
	}
	return ManagedLayout{
		DataDir:     dataDir,
		ArchivesDir: filepath.Join(dataDir, archivesDir),
		Roots:       roots,
	}, nil
}

// Provision creates the managed layout on disk — the data dir and every
// per-source archive root — with owner-only (0700) permissions, and returns the
// resolved layout. It is idempotent (MkdirAll): calling it on every launch is
// the SPEC-0013 "First launch creates the managed layout with no prompts"
// behavior, and a re-launch against an existing layout is a no-op that still
// returns the paths. It never prompts and never accepts a client path — only
// the app-owned dataDir.
func Provision(dataDir string) (ManagedLayout, error) {
	layout, err := ComputeLayout(dataDir)
	if err != nil {
		return ManagedLayout{}, err
	}
	// MkdirAll on each root also creates DataDir and ArchivesDir as parents, so
	// the whole layout is provisioned by these calls.
	for _, src := range source.All {
		if err := os.MkdirAll(layout.Roots[src], managedDirPerm); err != nil {
			return ManagedLayout{}, fmt.Errorf("setup: create managed root for %s: %w", src, err)
		}
	}
	return layout, nil
}
