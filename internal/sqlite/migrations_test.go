package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// isBlobType returns true for "BLOB" and "BLOB(N)" declared types. SQLite
// preserves the declared type verbatim in pragma_table_info, so the column
// "BLOB(16)" we use for UUIDv7 PKs is reported with the size annotation.
func isBlobType(s string) bool {
	upper := strings.ToUpper(strings.TrimSpace(s))
	return upper == "BLOB" || strings.HasPrefix(upper, "BLOB(")
}

func openFreshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable fk: %v", err)
	}
	if _, err := db.Exec(SchemaDDL); err != nil {
		t.Fatalf("apply base schema: %v", err)
	}
	return db
}

func tableInfo(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT name, type FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, ctype string
		if err := rows.Scan(&name, &ctype); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = ctype
	}
	return out
}

func TestApplyMigrations_CreatesAttemptsWithUUIDSchema(t *testing.T) {
	db := openFreshDB(t)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	cols := tableInfo(t, db, "attempts")
	for _, want := range []string{"id", "question_id", "selected", "is_correct", "attempted_at", "host_id"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("attempts missing column %q (have: %v)", want, cols)
		}
	}
	if got := cols["id"]; !isBlobType(got) {
		t.Errorf("attempts.id type = %q, want BLOB / BLOB(N)", got)
	}
}

func TestApplyMigrations_CreatesExplanationTablesWithUUIDSchema(t *testing.T) {
	db := openFreshDB(t)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	threadCols := tableInfo(t, db, "explanation_threads")
	for _, want := range []string{"id", "question_id", "status", "agent_session_id", "created_at", "closed_at", "updated_at", "host_id"} {
		if _, ok := threadCols[want]; !ok {
			t.Errorf("explanation_threads missing column %q (have: %v)", want, threadCols)
		}
	}
	if got := threadCols["id"]; !isBlobType(got) {
		t.Errorf("explanation_threads.id type = %q, want BLOB / BLOB(N)", got)
	}

	msgCols := tableInfo(t, db, "explanation_messages")
	for _, want := range []string{"id", "thread_id", "role", "author", "content", "reason_code", "citations", "translation_diff", "created_at", "host_id"} {
		if _, ok := msgCols[want]; !ok {
			t.Errorf("explanation_messages missing column %q (have: %v)", want, msgCols)
		}
	}
	if got := msgCols["id"]; !isBlobType(got) {
		t.Errorf("explanation_messages.id type = %q, want BLOB / BLOB(N)", got)
	}
	if got := msgCols["thread_id"]; !isBlobType(got) {
		t.Errorf("explanation_messages.thread_id type = %q, want BLOB / BLOB(N)", got)
	}
}

func TestApplyMigrations_RecordsSchemaVersion(t *testing.T) {
	db := openFreshDB(t)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = 3").Scan(&n); err != nil {
		t.Fatalf("count schema_version v3: %v", err)
	}
	if n != 1 {
		t.Errorf("schema_version v3 row count = %d, want 1", n)
	}
}

func TestApplyMigrations_Idempotent(t *testing.T) {
	db := openFreshDB(t)
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_version WHERE version = 3").Scan(&n); err != nil {
		t.Fatalf("count schema_version v3: %v", err)
	}
	if n != 1 {
		t.Errorf("schema_version v3 inserted twice (count=%d)", n)
	}
}

func TestApplyMigrations_DropsLegacyAttemptsAndRecreates(t *testing.T) {
	db := openFreshDB(t)
	// Pre-create a legacy-style attempts table with INTEGER PK to verify
	// that migration 003 drops it and replaces with the UUID schema.
	legacyDDL := `
		CREATE TABLE attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			question_id INTEGER NOT NULL,
			selected TEXT NOT NULL,
			is_correct INTEGER NOT NULL,
			attempted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatalf("create legacy attempts: %v", err)
	}
	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	cols := tableInfo(t, db, "attempts")
	if got := cols["id"]; !isBlobType(got) {
		t.Errorf("attempts.id after migration = %q, want BLOB / BLOB(N) (legacy not dropped)", got)
	}
	if _, ok := cols["host_id"]; !ok {
		t.Error("attempts missing host_id after migration (legacy not replaced)")
	}
}
