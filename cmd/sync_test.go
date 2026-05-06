package main

import (
	"bytes"
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
