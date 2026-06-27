package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joestump/sigbrowse/internal/config"
	"github.com/joestump/sigbrowse/internal/store"
)

// dbFileName is the SQLite database file within the data directory.
const dbFileName = "sigbrowse.sqlite"

// dbPath returns the absolute path to the SQLite database for the given config.
func dbPath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, dbFileName)
}

// openStore ensures the data directory exists and opens the database. Callers own
// Close. The data directory is created (the archive is never written to).
func openStore(cfg *config.Config) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", cfg.DataDir, err)
	}
	return store.Open(dbPath(cfg))
}

// requireArchive verifies the archive root is configured and present.
func requireArchive(cfg *config.Config) error {
	if cfg.ArchiveRoot == "" {
		return fmt.Errorf("archive_root is not set (use --archive-root, config, or SIGBROWSE_ARCHIVE_ROOT)")
	}
	info, err := os.Stat(cfg.ArchiveRoot)
	if err != nil {
		return fmt.Errorf("archive_root %q: %w", cfg.ArchiveRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("archive_root %q is not a directory", cfg.ArchiveRoot)
	}
	return nil
}
