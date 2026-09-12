package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// OpenOpts customizes how OpenWith applies migrations to a DB. The
// zero value preserves the historical Open(path) behaviour: no file
// backup, hostID derived from os.Hostname() as a fallback.
//
// HostID is stamped into rows preserved from the legacy v2 schema
// during migration 003 (multihost sync). Production callers should
// supply config.HostID so all rows on this machine carry a stable
// attribution; tests typically leave it empty.
//
// Backup, when true, takes a VACUUM INTO snapshot of the file at
// "<path>.pre-003.bak" before applying migration 003. The snapshot
// is a one-shot safety net — if it already exists it is preserved as
// the user's chosen backup state, never overwritten.
type OpenOpts struct {
	HostID string
	Backup bool
}

// OpenWith opens the DB at path with the given options. Open(path)
// is a thin wrapper around OpenWith(path, OpenOpts{}).
func OpenWith(path string, opts OpenOpts) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable fk: %w", err)
	}
	if _, err := db.Exec(SchemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateQuestionsAddColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if opts.Backup {
		if err := backupBeforeV3(db, path); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pre-v3 backup: %w", err)
		}
	}
	hostID := opts.HostID
	if hostID == "" {
		hostID = fallbackHostID()
	}
	if err := ApplyMigrationsForHost(db, hostID); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return db, nil
}

// backupBeforeV3 writes a VACUUM INTO snapshot to "<path>.pre-003.bak"
// when migration 003 has not yet been applied to this DB. The check
// is conservative: if the bak file already exists we leave it as-is
// (a previous run, or the user's hand-made backup, takes precedence).
// VACUUM INTO produces a consistent copy regardless of WAL state and
// is the same primitive used by `examtopicsdl sync snapshot`.
func backupBeforeV3(db *sql.DB, path string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_version: %w", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = 3").Scan(&n); err != nil {
		return fmt.Errorf("query schema_version: %w", err)
	}
	if n > 0 {
		return nil
	}
	bakPath := path + ".pre-003.bak"
	if _, err := os.Stat(bakPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	q := "VACUUM INTO " + quoteSQLiteString(bakPath)
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("vacuum into %s: %w", bakPath, err)
	}
	return nil
}

// quoteSQLiteString escapes a string for use as a SQL literal in DDL
// statements that don't accept bound parameters (e.g. VACUUM INTO).
func quoteSQLiteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// fallbackHostID synthesizes a host id from os.Hostname() for callers
// that didn't pass one explicitly. Used so preserved rows still carry
// a non-empty host_id even when a test or adhoc tool calls Open(path)
// without going through internal/config.
func fallbackHostID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	host = strings.ToLower(host)
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "host"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out + "-fallback"
}
