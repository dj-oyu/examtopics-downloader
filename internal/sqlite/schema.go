// Package sqlite owns the canonical schema for question/choice/discussion
// tables and provides the writer the Go scraper uses to persist exam data.
//
// Schema invariant (see AGENTS.md): three writers — this package,
// tools/md_to_sqlite.py, and web/src/db.ts — share the same SQLite file.
// This package writes only English-side columns and never touches *_ja
// columns or the explanation_threads/explanation_messages tables.
package sqlite

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// SchemaDDL is the canonical DDL for tables owned by the Go scraper.
// Tables created here:
//   - questions   (English columns + new cache-derived metadata)
//   - choices     (label + English text only; text_ja owned by translate.py)
//   - discussion  (per-poster split, populated only on cache path)
//
// Tables NOT created here (owned elsewhere):
//   - attempts, explanation_threads, explanation_messages → web/src/db.ts
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS questions (
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
    comments TEXT,
    exam_id INTEGER,
    is_mc INTEGER,
    answer_description TEXT,
    question_images TEXT,
    answer_images TEXT,
    content_hash TEXT,
    imported_at TEXT DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS choices (
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    text TEXT NOT NULL,
    text_ja TEXT,
    PRIMARY KEY (question_id, label)
);

CREATE TABLE IF NOT EXISTS discussion (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    poster TEXT,
    content TEXT NOT NULL,
    upvote_count INTEGER,
    posted_at TEXT,
    UNIQUE(question_id, idx)
);

CREATE INDEX IF NOT EXISTS idx_questions_exam ON questions(exam);
CREATE INDEX IF NOT EXISTS idx_questions_topic ON questions(topic, question_number);
CREATE INDEX IF NOT EXISTS idx_questions_url ON questions(url);
CREATE INDEX IF NOT EXISTS idx_discussion_qid ON discussion(question_id);
`

// addedColumns enumerates the columns added to questions on top of the
// original md_to_sqlite.py schema. Names must match the DDL above.
//
// Note: SQLite ALTER TABLE rejects non-constant defaults like
// CURRENT_TIMESTAMP, so the migrate path uses no DEFAULT for imported_at.
// Fresh CREATE TABLE keeps DEFAULT CURRENT_TIMESTAMP. Rows in legacy DBs
// that pre-date this column simply hold NULL until rewritten.
var addedColumns = []struct {
	name string
	ddl  string
}{
	{"exam_id", "INTEGER"},
	{"is_mc", "INTEGER"},
	{"answer_description", "TEXT"},
	{"question_images", "TEXT"},
	{"answer_images", "TEXT"},
	{"content_hash", "TEXT"},
	{"imported_at", "TEXT"},
}

// Open opens (creating if necessary) a SQLite DB at path, applies the canonical
// schema, and runs idempotent ALTER TABLE migrations to bring legacy DBs in
// line with the current shape. Safe to call repeatedly on the same file.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable fk: %w", err)
	}
	if _, err := db.Exec(SchemaDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateQuestionsAddColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateQuestionsAddColumns(db *sql.DB) error {
	existing, err := pragmaTableInfo(db, "questions")
	if err != nil {
		return fmt.Errorf("read table_info(questions): %w", err)
	}
	for _, c := range addedColumns {
		if _, ok := existing[c.name]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE questions ADD COLUMN %s %s", c.name, c.ddl)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	return nil
}

func pragmaTableInfo(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}
