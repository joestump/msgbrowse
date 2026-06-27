package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrate brings the database forward from whatever schema version it is on
// today to schemaVersion, applying each pending migration in order.
//
// Each migration runs inside its own transaction; foreign keys are toggled OFF
// for the duration of the migration (SQLite's recommended pattern when an
// ALTER may rebuild a referenced table) and back ON afterward, before commit
// recreates the regular constraint regime. Setting `PRAGMA user_version` is
// part of the same transaction, so a crashed apply leaves the version
// unchanged and the next Open retries from the same point.
//
// For a fresh database (version 0), every migration runs in order — there is
// no "current full schema" shortcut, so reasoning about how any database
// reached its current state is uniform.
func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database is at schema version %d, newer than this binary supports (%d)", current, schemaVersion)
	}
	for v := current + 1; v <= schemaVersion; v++ {
		// Guard the slice access so a forgotten registration (bumped
		// schemaVersion without appending to migrations) yields the intended
		// clean error instead of an index-out-of-range panic.
		if v >= len(migrations) || migrations[v] == "" {
			return fmt.Errorf("no migration registered for version %d", v)
		}
		if err := s.applyMigration(ctx, v, migrations[v]); err != nil {
			return fmt.Errorf("migration v%d: %w", v, err)
		}
	}
	return nil
}

// applyMigration runs one version's SQL in a transaction with foreign-key
// enforcement disabled (SQLite's recommended pattern for ALTERs that rebuild a
// referenced table). The named return lets the deferred FK-restore surface its
// own failure so a connection can never be returned to the pool with
// enforcement silently left off.
func (s *Store) applyMigration(ctx context.Context, version int, sqlText string) (err error) {
	// Foreign keys are a connection-scoped pragma and cannot be toggled inside
	// a transaction. Toggle outside the tx, on the same connection — we use a
	// dedicated Conn so concurrent readers in the pool aren't affected.
	conn, cerr := s.db.Conn(ctx)
	if cerr != nil {
		return cerr
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	// Verify the pragma actually took effect BEFORE running any destructive
	// DDL. A table rebuild (DROP + RENAME of a referenced table) that runs with
	// enforcement still ON would cascade-delete every child row and commit
	// silently. If it is not off, refuse to proceed.
	var fkEnabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if fkEnabled != 0 {
		return fmt.Errorf("refusing to run migration v%d: foreign_keys did not disable", version)
	}
	// Always attempt to restore enforcement; surface a restore failure (when
	// the migration itself otherwise succeeded) so the caller aborts Open
	// rather than handing back a FK-disabled connection.
	defer func() {
		if _, rerr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); rerr != nil && err == nil {
			err = fmt.Errorf("restore foreign_keys: %w", rerr)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("apply migration body: %w", err)
	}
	// Belt-and-suspenders: with enforcement off, a buggy migration could leave
	// dangling references. foreign_key_check reports one row per violation (none
	// when clean); treat any as fatal so we never commit a corrupt graph.
	if err := checkNoForeignKeyViolations(ctx, tx, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	rollback = false
	return nil
}

// checkNoForeignKeyViolations runs PRAGMA foreign_key_check inside tx and
// returns an error if any dangling reference exists. It fully drains and closes
// the result set before returning so the caller can keep using tx.
func checkNoForeignKeyViolations(ctx context.Context, tx *sql.Tx, version int) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("migration v%d left dangling foreign key references", version)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check rows: %w", err)
	}
	return nil
}

// readUserVersion returns the database's current schema version. Useful for
// tests that need to assert the migration ran.
func readUserVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
