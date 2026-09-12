package sync

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"examtopics-downloader/internal/sqlite"
	"examtopics-downloader/internal/uuidx"
	_ "modernc.org/sqlite"
)

func openMigratedDB(t *testing.T, name string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func seedQuestion(t *testing.T, db *sql.DB) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('TEST', 1, 1, 'Q', 'A', ?)`, "https://e/q-"+t.Name())
	if err != nil {
		t.Fatalf("seed q: %v", err)
	}
	qid, _ := res.LastInsertId()
	return int(qid)
}

func insertAttempt(t *testing.T, db *sql.DB, hostID string, qid int, selected string) [16]byte {
	t.Helper()
	id, err := uuidx.New()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO attempts(id, question_id, selected, is_correct, attempted_at, host_id) VALUES(?, ?, ?, 1, ?, ?)`,
		id[:], qid, selected, time.Now().UTC().Format(time.RFC3339), hostID); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	return id
}

func insertThread(t *testing.T, db *sql.DB, hostID string, qid int, status, updatedAt string) [16]byte {
	t.Helper()
	id, err := uuidx.New()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO explanation_threads(id, question_id, status, created_at, updated_at, host_id) VALUES(?, ?, ?, ?, ?, ?)`,
		id[:], qid, status, updatedAt, updatedAt, hostID); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	return id
}

func TestMerge_AppendsAttemptsFromPeer(t *testing.T) {
	dst, _ := openMigratedDB(t, "dst.db")
	src, srcPath := openMigratedDB(t, "src.db")
	qid := seedQuestion(t, dst)
	_ = seedQuestion(t, src)
	insertAttempt(t, src, "peer-host", qid, "A")
	insertAttempt(t, src, "peer-host", qid, "B")
	_ = src.Close()

	if err := Merge(dst, srcPath); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM attempts WHERE host_id='peer-host'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("attempts after merge = %d, want 2", n)
	}
}

func TestMerge_Idempotent(t *testing.T) {
	dst, _ := openMigratedDB(t, "dst.db")
	src, srcPath := openMigratedDB(t, "src.db")
	qid := seedQuestion(t, dst)
	_ = seedQuestion(t, src)
	insertAttempt(t, src, "peer-host", qid, "A")
	_ = src.Close()

	if err := Merge(dst, srcPath); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if err := Merge(dst, srcPath); err != nil {
		t.Fatalf("second merge: %v", err)
	}
	var n int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("idempotency violated: attempts after 2x merge = %d, want 1", n)
	}
}

func TestMerge_LWWThreadStatusUsesNewerUpdatedAt(t *testing.T) {
	dst, _ := openMigratedDB(t, "dst.db")
	src, srcPath := openMigratedDB(t, "src.db")
	qid := seedQuestion(t, dst)
	_ = seedQuestion(t, src)

	older := "2026-01-01T00:00:00Z"
	newer := "2026-06-01T00:00:00Z"

	tid := insertThread(t, dst, "host-A", qid, "open", older)
	// src has the SAME thread id with a newer updated_at and resolved status
	if _, err := src.Exec(`INSERT INTO explanation_threads(id, question_id, status, created_at, updated_at, host_id) VALUES(?, ?, 'resolved', ?, ?, 'host-B')`,
		tid[:], qid, older, newer); err != nil {
		t.Fatalf("seed src thread: %v", err)
	}
	_ = src.Close()

	if err := Merge(dst, srcPath); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	var status, updatedAt string
	if err := dst.QueryRow(`SELECT status, updated_at FROM explanation_threads WHERE id = ?`, tid[:]).Scan(&status, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" {
		t.Errorf("status = %q after LWW, want resolved (newer src update should win)", status)
	}
	if updatedAt != newer {
		t.Errorf("updated_at = %q after LWW, want %q", updatedAt, newer)
	}
}

func TestMerge_LWWPreservesNewerLocal(t *testing.T) {
	dst, _ := openMigratedDB(t, "dst.db")
	src, srcPath := openMigratedDB(t, "src.db")
	qid := seedQuestion(t, dst)
	_ = seedQuestion(t, src)

	older := "2026-01-01T00:00:00Z"
	newer := "2026-06-01T00:00:00Z"

	tid := insertThread(t, dst, "host-A", qid, "resolved", newer)
	// src has the SAME thread id but older timestamp — must NOT overwrite
	if _, err := src.Exec(`INSERT INTO explanation_threads(id, question_id, status, created_at, updated_at, host_id) VALUES(?, ?, 'open', ?, ?, 'host-B')`,
		tid[:], qid, older, older); err != nil {
		t.Fatalf("seed src thread: %v", err)
	}
	_ = src.Close()

	if err := Merge(dst, srcPath); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	var status string
	if err := dst.QueryRow(`SELECT status FROM explanation_threads WHERE id = ?`, tid[:]).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" {
		t.Errorf("status = %q, want resolved (older src should not overwrite)", status)
	}
}

func TestSnapshot_ProducesReopenableDB(t *testing.T) {
	src, srcPath := openMigratedDB(t, "live.db")
	qid := seedQuestion(t, src)
	insertAttempt(t, src, "host-A", qid, "A")

	out := filepath.Join(t.TempDir(), "snap.db")
	if err := Snapshot(srcPath, out); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	var n int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&n); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if n != 1 {
		t.Errorf("snapshot attempts = %d, want 1", n)
	}
}

func TestContentSync_OverwritesQuestionsFromMaster(t *testing.T) {
	dst, _ := openMigratedDB(t, "sbc.db")
	src, srcPath := openMigratedDB(t, "master.db")
	// Master has 2 questions, sbc has 1 (stale).
	if _, err := src.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('M', 1, 1, 'm1', 'A', 'https://e/m1'), ('M', 1, 2, 'm2', 'B', 'https://e/m2')`); err != nil {
		t.Fatalf("seed master: %v", err)
	}
	if _, err := dst.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('OLD', 1, 99, 'stale', 'C', 'https://e/old')`); err != nil {
		t.Fatalf("seed sbc: %v", err)
	}
	_ = src.Close()

	if err := ContentSync(dst, srcPath); err != nil {
		t.Fatalf("ContentSync: %v", err)
	}
	var stale int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM questions WHERE exam='OLD'`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("stale rows after content sync = %d, want 0", stale)
	}
	var fresh int
	if err := dst.QueryRow(`SELECT COUNT(*) FROM questions WHERE exam='M'`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh != 2 {
		t.Errorf("master rows after content sync = %d, want 2", fresh)
	}
}
