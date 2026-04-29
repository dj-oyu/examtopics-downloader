package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// columnsOf returns the set of column names for a SQLite table.
func columnsOf(t *testing.T, db *sql.DB, table string) map[string]struct{} {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = struct{}{}
	}
	return cols
}

func tablesIn(t *testing.T, db *sql.DB) map[string]struct{} {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	tabs := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tabs[name] = struct{}{}
	}
	return tabs
}

func TestOpen_CreatesAllOwnedTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tabs := tablesIn(t, db)
	for _, want := range []string{"questions", "choices", "discussion"} {
		if _, ok := tabs[want]; !ok {
			t.Errorf("expected table %q to exist after Open, tables=%v", want, tabs)
		}
	}
	// Tables we must NOT touch — these are owned by web/db.ts and translate.py.
	for _, owned := range []string{"attempts", "explanation_threads", "explanation_messages"} {
		if _, ok := tabs[owned]; ok {
			t.Errorf("Open should NOT create %q (owned elsewhere); found in fresh DB", owned)
		}
	}
}

func TestOpen_QuestionsHasAllNewColumns(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cols := columnsOf(t, db, "questions")
	for _, want := range []string{
		"id", "exam", "topic", "question_number",
		"question_text", "question_text_ja",
		"suggested_answer", "confirmed_answer",
		"explanation_ja", "timestamp", "url", "comments",
		"exam_id", "is_mc", "answer_description",
		"question_images", "answer_images",
		"content_hash", "imported_at",
	} {
		if _, ok := cols[want]; !ok {
			t.Errorf("questions missing column %q (have: %v)", want, cols)
		}
	}
}

func TestOpen_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twice.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	cols := columnsOf(t, db2, "questions")
	if _, ok := cols["content_hash"]; !ok {
		t.Errorf("second Open lost content_hash column")
	}
}

// Critical regression: opening a "legacy" DB that only carries the original
// md_to_sqlite.py columns must add the new columns AND preserve any seeded _ja
// data without rewriting rows.
func TestOpen_MigratesLegacyDBPreservesJa(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Hand-build a legacy DB with the pre-extension schema and a seeded row.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	legacyDDL := `
		CREATE TABLE questions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exam TEXT NOT NULL,
			topic INTEGER NOT NULL,
			question_number INTEGER NOT NULL,
			question_text TEXT NOT NULL,
			question_text_ja TEXT,
			suggested_answer TEXT,
			confirmed_answer TEXT,
			explanation_ja TEXT,
			timestamp TEXT,
			url TEXT UNIQUE,
			comments TEXT
		);
		CREATE TABLE choices (
			question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
			label TEXT NOT NULL,
			text TEXT NOT NULL,
			text_ja TEXT,
			PRIMARY KEY (question_id, label)
		);
	`
	if _, err := legacy.Exec(legacyDDL); err != nil {
		t.Fatalf("legacy DDL: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO questions(
		exam, topic, question_number, question_text, question_text_ja,
		suggested_answer, url
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"AWS Certified Developer Associate DVA C02", 1, 1,
		"What is the question?", "これは問題ですか?",
		"BD", "https://example.com/q1",
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO choices(question_id, label, text, text_ja)
		VALUES (1, 'A', 'first', '一番'), (1, 'B', 'second', '二番')`); err != nil {
		t.Fatalf("seed choices: %v", err)
	}
	legacy.Close()

	// Run our Open() — should ALTER TABLE to add new cols, leave _ja alone.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open after legacy: %v", err)
	}
	defer db.Close()

	cols := columnsOf(t, db, "questions")
	for _, want := range []string{"exam_id", "is_mc", "answer_description", "content_hash", "imported_at"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("migration did not add %q (have: %v)", want, cols)
		}
	}

	// _ja preserved
	var qja, txtJa string
	if err := db.QueryRow("SELECT question_text_ja FROM questions WHERE id=1").Scan(&qja); err != nil {
		t.Fatalf("read question_text_ja: %v", err)
	}
	if qja != "これは問題ですか?" {
		t.Errorf("question_text_ja drifted: got %q", qja)
	}
	if err := db.QueryRow("SELECT text_ja FROM choices WHERE question_id=1 AND label='A'").Scan(&txtJa); err != nil {
		t.Fatalf("read choices.text_ja: %v", err)
	}
	if txtJa != "一番" {
		t.Errorf("choices.text_ja drifted: got %q", txtJa)
	}

	// New tables (discussion) appear too
	tabs := tablesIn(t, db)
	if _, ok := tabs["discussion"]; !ok {
		t.Errorf("discussion table not created during migration; tables=%v", tabs)
	}
}
