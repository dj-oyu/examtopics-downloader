package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"examtopics-downloader/internal/sqlite"
)

func TestRunSyncTo_NoSubcommandReturnsUsage(t *testing.T) {
	var buf bytes.Buffer
	exit := runSyncTo(&buf, nil)
	if exit != 2 {
		t.Errorf("exit = %d, want 2 (usage)", exit)
	}
	if !strings.Contains(buf.String(), "subcommand required") {
		t.Errorf("missing usage hint\n%s", buf.String())
	}
}

func TestRunSyncTo_UnknownSubcommandReturnsUsage(t *testing.T) {
	var buf bytes.Buffer
	exit := runSyncTo(&buf, []string{"noop"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
}

func TestRunSyncTo_SnapshotProducesReadableFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "live.db")
	db, err := sqlite.Open(srcPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url) VALUES('S', 1, 1, 'q', 'A', 'https://e/q')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	outPath := filepath.Join(dir, "snap.db")
	var buf bytes.Buffer
	exit := runSyncTo(&buf, []string{"snapshot", "-d", srcPath, "-o", outPath})
	if exit != 0 {
		t.Fatalf("exit = %d\n%s", exit, buf.String())
	}
	snap, err := sqlite.Open(outPath)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	var n int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM questions WHERE exam='S'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("snapshot rows = %d, want 1", n)
	}
}

// --- sync translations ---------------------------------------------------

func TestRunSyncTo_TranslationsRequiresFlags(t *testing.T) {
	var buf bytes.Buffer
	if exit := runSyncTo(&buf, []string{"translations"}); exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(buf.String(), "required") {
		t.Errorf("missing usage hint\n%s", buf.String())
	}
}

func TestRunSyncTo_TranslationsRejectsUnknownPrefer(t *testing.T) {
	var buf bytes.Buffer
	exit := runSyncTo(&buf, []string{"translations", "-d", "a.db", "--from", "b.db", "--prefer", "newest"})
	if exit != 2 {
		t.Fatalf("exit = %d, want 2\n%s", exit, buf.String())
	}
	if !strings.Contains(buf.String(), "must be local or peer") {
		t.Errorf("unhelpful error\n%s", buf.String())
	}
}

func TestRunSyncTo_TranslationsPullsPeerWording(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "local.db")
	peerPath := filepath.Join(dir, "peer.db")

	local, err := sqlite.Open(localPath)
	if err != nil {
		t.Fatalf("open local: %v", err)
	}
	if _, err := local.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url)
		VALUES('S', 1, 1, 'q', 'A', 'https://e/q')`); err != nil {
		t.Fatal(err)
	}
	_ = local.Close()

	peer, err := sqlite.Open(peerPath)
	if err != nil {
		t.Fatalf("open peer: %v", err)
	}
	if _, err := peer.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url, question_text_ja, explanation_ja)
		VALUES('S', 1, 1, 'q', 'A', 'https://e/q', 'ピアの訳', 'ピアの解説')`); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()

	var buf bytes.Buffer
	exit := runSyncTo(&buf, []string{"translations", "-d", localPath, "--from", peerPath, "--allow-wal"})
	if exit != 0 {
		t.Fatalf("exit = %d\n%s", exit, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"filled question fields: 2", "conflicts: none"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	check, err := sqlite.Open(localPath)
	if err != nil {
		t.Fatalf("reopen local: %v", err)
	}
	defer func() { _ = check.Close() }()
	var qJa string
	if err := check.QueryRow(`SELECT question_text_ja FROM questions WHERE url='https://e/q'`).Scan(&qJa); err != nil {
		t.Fatal(err)
	}
	if qJa != "ピアの訳" {
		t.Errorf("question_text_ja = %q, want the peer's wording", qJa)
	}
}

func TestRunSyncTo_TranslationsDryRunReportsOnly(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "local.db")
	peerPath := filepath.Join(dir, "peer.db")

	local, err := sqlite.Open(localPath)
	if err != nil {
		t.Fatalf("open local: %v", err)
	}
	if _, err := local.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url)
		VALUES('S', 1, 1, 'q', 'A', 'https://e/q')`); err != nil {
		t.Fatal(err)
	}
	_ = local.Close()

	peer, err := sqlite.Open(peerPath)
	if err != nil {
		t.Fatalf("open peer: %v", err)
	}
	if _, err := peer.Exec(`INSERT INTO questions(exam, topic, question_number, question_text, suggested_answer, url, question_text_ja)
		VALUES('S', 1, 1, 'q', 'A', 'https://e/q', 'ピアの訳')`); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()

	var buf bytes.Buffer
	exit := runSyncTo(&buf, []string{"translations", "-d", localPath, "--from", peerPath, "--allow-wal", "--dry-run"})
	if exit != 0 {
		t.Fatalf("exit = %d\n%s", exit, buf.String())
	}
	if !strings.Contains(buf.String(), "dry run") {
		t.Errorf("dry run should say so\n%s", buf.String())
	}
	check, err := sqlite.Open(localPath)
	if err != nil {
		t.Fatalf("reopen local: %v", err)
	}
	defer func() { _ = check.Close() }()
	var v sql.NullString
	if err := check.QueryRow(`SELECT question_text_ja FROM questions WHERE url='https://e/q'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v.Valid {
		t.Errorf("dry run wrote %q", v.String)
	}
}
