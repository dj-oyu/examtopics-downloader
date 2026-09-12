package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"examtopics-downloader/internal/sqlite"
	_ "modernc.org/sqlite"
)

func seedQuizDB(t *testing.T, db *sql.DB) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('TEST', 1, 1, 'pick A', 'A', 'https://e/q1')`)
	if err != nil {
		t.Fatalf("seed q1: %v", err)
	}
	qid, _ := res.LastInsertId()
	for label, text := range map[string]string{"A": "right", "B": "wrong"} {
		if _, err := db.Exec(`INSERT INTO choices(question_id, label, text) VALUES(?, ?, ?)`, qid, label, text); err != nil {
			t.Fatalf("seed choice %s: %v", label, err)
		}
	}
}

func TestRunQuizTo_RecordsAttemptForGivenAnswer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quiz.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedQuizDB(t, db)
	_ = db.Close()

	var out bytes.Buffer
	in := strings.NewReader("A\n")
	exit := runQuizTo(in, &out, "test-host", []string{"-db", dbPath})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\noutput:\n%s", exit, out.String())
	}
	if !strings.Contains(out.String(), "Correct") {
		t.Errorf("output missing Correct marker\n%s", out.String())
	}

	// Re-open and confirm the attempt landed in the table.
	db2, _ := sqlite.Open(dbPath)
	defer func() { _ = db2.Close() }()
	var n, correct int
	if err := db2.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_correct),0) FROM attempts WHERE host_id='test-host'`).Scan(&n, &correct); err != nil {
		t.Fatal(err)
	}
	if n != 1 || correct != 1 {
		t.Errorf("attempts count=%d correct=%d, want 1/1", n, correct)
	}
}

func TestRunQuizTo_StopsAtEOF(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quiz.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedQuizDB(t, db)
	// Add a second question so we can confirm EOF stops the loop early.
	_, err = db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('TEST', 1, 2, 'pick B', 'B', 'https://e/q2')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var out bytes.Buffer
	in := strings.NewReader("A\n") // only one answer for two questions
	exit := runQuizTo(in, &out, "test-host", []string{"-db", dbPath})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\noutput:\n%s", exit, out.String())
	}
	db2, _ := sqlite.Open(dbPath)
	defer func() { _ = db2.Close() }()
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM attempts WHERE host_id='test-host'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("attempts count = %d, want 1 (EOF should halt before second question)", n)
	}
}
