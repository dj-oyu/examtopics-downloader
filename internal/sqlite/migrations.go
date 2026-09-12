package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// migrationFS embeds the SQL migration scripts that the Go side knows how to
// apply. Each file is named "NNN_description.sql"; ApplyMigrations parses the
// numeric prefix as the migration version and applies them in order, skipping
// any version already recorded in the schema_version table.
//
// Web (Bun) reads its own copy of historical migrations from <repo>/migrations/
// for backward compatibility on existing user DBs that started with the
// legacy INTEGER-PK schema (versions 001 / 002). Go starts fresh at version
// 003 and never replays the earlier migrations because the legacy tables they
// modify are dropped wholesale by 003 anyway.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

var migrationFileRe = regexp.MustCompile(`^(\d+)_(.*)\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations returns the embedded migrations sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		data, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: m[2], sql: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// ApplyMigrations applies every embedded migration that has not yet been
// recorded in the schema_version table. It is safe to call repeatedly: the
// runner skips already-applied versions. Each migration runs inside its own
// transaction with foreign keys disabled so 12-step table reconstruction
// scripts can drop and recreate parents without dangling FKs.
//
// Use ApplyMigrationsForHost when running against a DB that may carry
// legacy v2 multihost rows that should be preserved through the v3
// migration with a particular host_id stamp. ApplyMigrations defaults
// the host_id to a hostname-derived fallback for callers that don't
// have config in scope (typically tests).
func ApplyMigrations(db *sql.DB) error {
	return ApplyMigrationsForHost(db, fallbackHostID())
}

// ApplyMigrationsForHost is the variant used by Open* paths that have
// loaded config and know the canonical host_id. The id is stamped into
// rows preserved from the legacy v2 schema during migration 003.
func ApplyMigrationsForHost(db *sql.DB, hostID string) error {
	if hostID == "" {
		hostID = fallbackHostID()
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_version: %w", err)
	}

	applied, err := loadAppliedVersions(db)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyOne(db, m, hostID); err != nil {
			return fmt.Errorf("migration %03d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

func loadAppliedVersions(db *sql.DB) (map[int]struct{}, error) {
	rows, err := db.Query("SELECT version FROM schema_version")
	if err != nil {
		return nil, fmt.Errorf("query schema_version: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int]struct{}{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

// applyOne runs a single migration with foreign keys off so DROP/CREATE
// sequences that affect FK relationships can complete cleanly. The
// schema_version row is inserted in the same transaction so a partial
// migration cannot leave the version marker out of sync with the schema.
//
// Migration 003 (multihost sync) gets a special wrap: legacy v2 rows
// are captured before the SQL drops them and reinserted with fresh
// UUIDv7 ids and host_id = the supplied hostID after the new tables
// exist, all in the same transaction. captureLegacyV2 / restoreLegacyV2
// live in preserve_v3.go and are no-ops when there is nothing to
// migrate (fresh DB, or already at v3).
func applyOne(db *sql.DB, m migration, hostID string) error {
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}
	defer func() { _, _ = db.Exec("PRAGMA foreign_keys = ON") }()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	var captured *capturedV2
	if m.version == 3 {
		captured, err = captureLegacyV2(tx)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("capture legacy v2: %w", err)
		}
	}

	if _, err := tx.Exec(m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("exec script: %w", err)
	}

	if m.version == 3 && captured != nil {
		if err := restoreLegacyV2(tx, captured, hostID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("restore legacy v2: %w", err)
		}
	}

	if _, err := tx.Exec("INSERT INTO schema_version(version, name) VALUES(?, ?)", m.version, m.name); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
